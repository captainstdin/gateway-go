package gateway_api

import (
	"encoding/json"
	"fmt"
	gwctx "github.com/captainstdin/gateway-go/pkg/context"
	"github.com/captainstdin/gateway-go/pkg/protocol"
	"github.com/captainstdin/gateway-go/pkg/worker"
)

var bw *worker.BusinessWorker

func SetBusinessWorker(w *worker.BusinessWorker) { bw = w }

// ==================== 内部辅助方法 ====================

// sendCmd 向指定客户端发送命令（fire-and-forget）
func sendCmd(clientID string, cmd uint8, message []byte, extData string) {
	if bw == nil {
		return
	}
	localIP, localPort, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = cmd
	gd.ConnectionID = connID
	gd.Body = message
	gd.ExtData = extData
	gd.Flag = protocol.FlagBodyIsScalar
	bw.SendToGateway(addrStr(localIP, localPort), protocol.Encode(gd))
}

// sendCmdToAll 向所有 gateway 发送命令（fire-and-forget）
func sendCmdToAll(cmd uint8, message []byte, extData string) {
	if bw == nil {
		return
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = cmd
	gd.Body = message
	gd.ExtData = extData
	gd.Flag = protocol.FlagBodyIsScalar
	bw.SendToAllGateways(protocol.Encode(gd))
}

// addrStr 将 IP 和 Port 格式化为地址字符串
func addrStr(ip uint32, port uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", ip>>24, (ip>>16)&0xff, (ip>>8)&0xff, ip&0xff, port)
}

// clientIDArrayToAddressMap 将 clientID 数组转换为 addr -> []connID 映射
func clientIDArrayToAddressMap(clientIDs []string) map[string][]uint32 {
	result := make(map[string][]uint32)
	for _, cid := range clientIDs {
		ip, port, connID, err := gwctx.ClientIDToAddress(cid)
		if err != nil {
			continue
		}
		addr := addrStr(ip, port)
		result[addr] = append(result[addr], connID)
	}
	return result
}

// ==================== 发送类方法 ====================

// SendToClient 向某个 client_id 对应的连接发消息
// 对应 PHP Gateway::sendToClient
func SendToClient(clientID string, message []byte) {
	sendCmd(clientID, protocol.CmdSendToOne, message, "")
}

// SendToCurrentClient 向当前客户端连接发送消息（方案 A：等同于 SendToClient）
// 对应 PHP Gateway::sendToCurrentClient
func SendToCurrentClient(clientID string, message []byte) {
	SendToClient(clientID, message)
}

// SendToAll 向所有客户端连接广播消息
// 对应 PHP Gateway::sendToAll
// clientIDs: 仅向这些客户端发送（为空则全部）
// excludeClientIDs: 排除这些客户端
func SendToAll(message []byte, clientIDs []string, excludeClientIDs []string) {
	// 指定了目标 clientID 列表
	if len(clientIDs) > 0 {
		// 构建排除集合
		excludeSet := make(map[string]bool, len(excludeClientIDs))
		for _, eid := range excludeClientIDs {
			excludeSet[eid] = true
		}

		grouped := make(map[string][]uint32)
		for _, cid := range clientIDs {
			if excludeSet[cid] {
				continue
			}
			ip, port, connID, err := gwctx.ClientIDToAddress(cid)
			if err != nil {
				continue
			}
			addr := addrStr(ip, port)
			grouped[addr] = append(grouped[addr], connID)
		}
		for addr, connIDs := range grouped {
			ext, _ := json.Marshal(map[string]interface{}{"connections": connIDs})
			gd := protocol.NewEmptyData()
			gd.Cmd = protocol.CmdSendToAll
			gd.Body = message
			gd.ExtData = string(ext)
			gd.Flag = protocol.FlagBodyIsScalar
			bw.SendToGateway(addr, protocol.Encode(gd))
		}
		return
	}

	// 没有排除列表，直接广播
	if len(excludeClientIDs) == 0 {
		sendCmdToAll(protocol.CmdSendToAll, message, "")
		return
	}

	// 有排除列表，需要按 gateway 地址分组排除的 connectionID
	excludeByAddr := clientIDArrayToAddressMap(excludeClientIDs)
	allAddrs := getAllGatewayAddresses()
	for _, addr := range allAddrs {
		gd := protocol.NewEmptyData()
		gd.Cmd = protocol.CmdSendToAll
		gd.Body = message
		gd.Flag = protocol.FlagBodyIsScalar

		if connIDs, ok := excludeByAddr[addr]; ok {
			excludeMap := make(map[uint32]uint32, len(connIDs))
			for _, id := range connIDs {
				excludeMap[id] = id
			}
			ext, _ := json.Marshal(map[string]interface{}{"exclude": excludeMap})
			gd.ExtData = string(ext)
		}
		bw.SendToGateway(addr, protocol.Encode(gd))
	}
}

// SendToUID 向 uid 绑定的所有连接发送消息
// 对应 PHP Gateway::sendToUid
func SendToUID(uids []string, message []byte) {
	ext, _ := json.Marshal(uids)
	sendCmdToAll(protocol.CmdSendToUID, message, string(ext))
}

// SendToGroup 向 group 内的所有连接发送消息
// 对应 PHP Gateway::sendToGroup
// groups: 目标分组列表
// excludeClientIDs: 排除这些客户端
func SendToGroup(groups []string, message []byte, excludeClientIDs []string) {
	if len(groups) == 0 {
		return
	}

	// 无排除列表，直接发送
	if len(excludeClientIDs) == 0 {
		ext, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": nil})
		sendCmdToAll(protocol.CmdSendToGroup, message, string(ext))
		return
	}

	// 有排除列表，按 gateway 地址分组
	excludeByAddr := clientIDArrayToAddressMap(excludeClientIDs)
	defaultExt, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": nil})
	allAddrs := getAllGatewayAddresses()

	for _, addr := range allAddrs {
		gd := protocol.NewEmptyData()
		gd.Cmd = protocol.CmdSendToGroup
		gd.Body = message
		gd.Flag = protocol.FlagBodyIsScalar

		if connIDs, ok := excludeByAddr[addr]; ok {
			excludeMap := make(map[uint32]uint32, len(connIDs))
			for _, id := range connIDs {
				excludeMap[id] = id
			}
			ext, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": excludeMap})
			gd.ExtData = string(ext)
		} else {
			gd.ExtData = string(defaultExt)
		}
		bw.SendToGateway(addr, protocol.Encode(gd))
	}
}

// ==================== 绑定/分组方法 ====================

// BindUID 将 client_id 与 uid 绑定
func BindUID(clientID, uid string)     { sendCmd(clientID, protocol.CmdBindUID, nil, uid) }

// UnbindUID 将 client_id 与 uid 解除绑定
func UnbindUID(clientID, uid string)   { sendCmd(clientID, protocol.CmdUnbindUID, nil, uid) }

// JoinGroup 将 client_id 加入组
func JoinGroup(clientID, group string) { sendCmd(clientID, protocol.CmdJoinGroup, nil, group) }

// LeaveGroup 将 client_id 离开组
func LeaveGroup(clientID, group string) { sendCmd(clientID, protocol.CmdLeaveGroup, nil, group) }

// Ungroup 取消（解散）分组
func Ungroup(group string) { sendCmdToAll(protocol.CmdUngroup, nil, group) }

// ==================== Session 方法 ====================

// SetSession 设置 session，覆盖原有值
func SetSession(clientID string, session map[string]interface{}) {
	sendCmd(clientID, protocol.CmdSetSession, nil, gwctx.SessionEncode(session))
}

// UpdateSession 更新 session，与老 session 合并
func UpdateSession(clientID string, session map[string]interface{}) {
	sendCmd(clientID, protocol.CmdUpdateSession, nil, gwctx.SessionEncode(session))
}

// GetSession 获取某个 client_id 的 session
// 对应 PHP Gateway::getSession
func GetSession(clientID string) (map[string]interface{}, error) {
	ensurePool()

	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return nil, err
	}
	addr := addrStr(ip, port)

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetSessionByClientID
	gd.ConnectionID = connID

	data, err := pool.querySingleGateway(addr, gd)
	if err != nil {
		return nil, err
	}

	var session map[string]interface{}
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return session, nil
}

// ==================== 关闭/踢出方法 ====================

// CloseClient 踢掉某个客户端，可附带消息
func CloseClient(clientID string, message []byte) {
	sendCmd(clientID, protocol.CmdKick, message, "")
}

// CloseCurrentClient 踢掉当前客户端（方案 A：等同于 CloseClient）
func CloseCurrentClient(clientID string, message []byte) {
	CloseClient(clientID, message)
}

// DestroyClient 直接销毁某个客户端连接
func DestroyClient(clientID string) {
	sendCmd(clientID, protocol.CmdDestroy, nil, "")
}

// DestroyCurrentClient 直接销毁当前客户端连接（方案 A：等同于 DestroyClient）
func DestroyCurrentClient(clientID string) {
	DestroyClient(clientID)
}

// ==================== 在线状态查询 ====================

// IsOnline 判断 client_id 对应的连接是否在线
// 对应 PHP Gateway::isOnline
func IsOnline(clientID string) bool {
	ensurePool()

	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return false
	}
	addr := addrStr(ip, port)

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdIsOnline
	gd.ConnectionID = connID

	data, err := pool.querySingleGateway(addr, gd)
	if err != nil {
		return false
	}

	var result int
	json.Unmarshal(data, &result)
	return result == 1
}

// IsUidOnline 判断某个 uid 是否在线
// 对应 PHP Gateway::isUidOnline
func IsUidOnline(uid string) bool {
	clients := GetClientIdByUid(uid)
	return len(clients) > 0
}

// ==================== 数量统计方法 ====================

// GetAllClientCount 获取所有在线 client_id 数
// 对应 PHP Gateway::getAllClientCount / getAllClientIdCount
func GetAllClientCount() int {
	return GetClientCountByGroup("")
}

// GetClientCountByGroup 获取某个组的在线 client_id 数（group 为空则统计所有）
// 对应 PHP Gateway::getClientCountByGroup / getClientIdCountByGroup
func GetClientCountByGroup(group string) int {
	ensurePool()

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientCountByGroup
	gd.ExtData = group

	results := pool.queryAllGateways(gd)
	total := 0
	for _, r := range results {
		if r.err != nil {
			continue
		}
		var count int
		if json.Unmarshal(r.data, &count) == nil {
			total += count
		}
	}
	return total
}

// GetUidCountByGroup 获取某个组的在线 uid 数
// 对应 PHP Gateway::getUidCountByGroup
func GetUidCountByGroup(group string) int {
	return len(GetUidListByGroup(group))
}

// GetAllUidCount 获取全局在线 uid 数
// 对应 PHP Gateway::getAllUidCount
func GetAllUidCount() int {
	return len(GetAllUidList())
}

// GetAllGroupClientIdCount 获取所有群组在线 client_id 数量
// 对应 PHP Gateway::getAllGroupClientIdCount
func GetAllGroupClientIdCount() map[string]int {
	groupClientMap := GetAllGroupClientIdList()
	result := make(map[string]int, len(groupClientMap))
	for group, clients := range groupClientMap {
		result[group] = len(clients)
	}
	return result
}

// GetAllGroupUidCount 获取所有分组的在线 uid 数量
// 对应 PHP Gateway::getAllGroupUidCount
func GetAllGroupUidCount() map[string]int {
	groupUidMap := GetAllGroupUidList()
	result := make(map[string]int, len(groupUidMap))
	for group, uids := range groupUidMap {
		result[group] = len(uids)
	}
	return result
}

// ==================== 列表查询方法 ====================

// GetAllClientSessions 获取所有在线 client_id 的 session
// 对应 PHP Gateway::getAllClientSessions
func GetAllClientSessions(group string) map[string]map[string]interface{} {
	ensurePool()

	gd := protocol.NewEmptyData()
	if group == "" {
		gd.Cmd = protocol.CmdGetAllClientSessions
	} else {
		gd.Cmd = protocol.CmdGetClientSessionsByGroup
		gd.ExtData = group
	}

	results := pool.queryAllGateways(gd)
	statusData := make(map[string]map[string]interface{})

	for _, r := range results {
		if r.err != nil {
			continue
		}
		// 解析 gateway 地址获取 localIP, localPort
		ip, port := parseAddr(r.addr)

		var connSessions map[uint32]string
		if json.Unmarshal(r.data, &connSessions) != nil {
			continue
		}
		for connID, sessionStr := range connSessions {
			clientID := gwctx.AddressToClientID(ip, port, connID)
			if sessionStr != "" {
				statusData[clientID] = gwctx.SessionDecode(sessionStr)
			} else {
				statusData[clientID] = make(map[string]interface{})
			}
		}
	}
	return statusData
}

// GetClientSessionsByGroup 获取某个组的所有 client_id 的 session
// 对应 PHP Gateway::getClientSessionsByGroup
func GetClientSessionsByGroup(group string) map[string]map[string]interface{} {
	if group == "" {
		return nil
	}
	return GetAllClientSessions(group)
}

// GetAllClientIdList 获取所有在线 client_id 列表
// 对应 PHP Gateway::getAllClientIdList
func GetAllClientIdList() []string {
	data := selectQuery([]string{"uid"}, nil)
	return formatClientIdFromSelectData(data)
}

// GetClientIdListByGroup 获取某个群组在线 client_id 列表
// 对应 PHP Gateway::getClientIdListByGroup
func GetClientIdListByGroup(group string) []string {
	if group == "" {
		return nil
	}
	data := selectQuery([]string{"uid"}, map[string]interface{}{"groups": []string{group}})
	return formatClientIdFromSelectData(data)
}

// GetClientIdByUid 获取与 uid 绑定的 client_id 列表
// 对应 PHP Gateway::getClientIdByUid
func GetClientIdByUid(uid string) []string {
	ensurePool()

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientIDByUID
	gd.ExtData = uid

	results := pool.queryAllGateways(gd)
	var clientList []string

	for _, r := range results {
		if r.err != nil {
			continue
		}
		ip, port := parseAddr(r.addr)

		var connIDs []uint32
		if json.Unmarshal(r.data, &connIDs) != nil {
			continue
		}
		for _, connID := range connIDs {
			clientList = append(clientList, gwctx.AddressToClientID(ip, port, connID))
		}
	}
	return clientList
}

// GetClientIdByUids 批量获取多个 uid 绑定的 client_id 列表
// 对应 PHP Gateway::getClientIdByUids
func GetClientIdByUids(uids []string) map[string][]string {
	ensurePool()

	ext, _ := json.Marshal(uids)
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdBatchGetClientIDByUID
	gd.ExtData = string(ext)

	results := pool.queryAllGateways(gd)
	clientMap := make(map[string][]string)

	for _, r := range results {
		if r.err != nil {
			continue
		}
		ip, port := parseAddr(r.addr)

		var uidConnMap map[string][]uint32
		if json.Unmarshal(r.data, &uidConnMap) != nil {
			continue
		}
		for uid, connIDs := range uidConnMap {
			for _, connID := range connIDs {
				clientMap[uid] = append(clientMap[uid], gwctx.AddressToClientID(ip, port, connID))
			}
		}
	}
	return clientMap
}

// GetUidListByGroup 获取某个群组在线 uid 列表
// 对应 PHP Gateway::getUidListByGroup
func GetUidListByGroup(group string) []string {
	if group == "" {
		return nil
	}
	data := selectQuery([]string{"uid"}, map[string]interface{}{"groups": []string{group}})
	uidMap := make(map[string]bool)
	for _, info := range data {
		if uid, ok := info["uid"].(string); ok && uid != "" {
			uidMap[uid] = true
		}
	}
	uids := make([]string, 0, len(uidMap))
	for uid := range uidMap {
		uids = append(uids, uid)
	}
	return uids
}

// GetAllUidList 获取全局在线 uid 列表
// 对应 PHP Gateway::getAllUidList
func GetAllUidList() []string {
	data := selectQuery([]string{"uid"}, nil)
	uidMap := make(map[string]bool)
	for _, info := range data {
		if uid, ok := info["uid"].(string); ok && uid != "" {
			uidMap[uid] = true
		}
	}
	uids := make([]string, 0, len(uidMap))
	for uid := range uidMap {
		uids = append(uids, uid)
	}
	return uids
}

// GetUidByClientId 通过 client_id 获取 uid
// 对应 PHP Gateway::getUidByClientId
func GetUidByClientId(clientID string) string {
	data := selectQuery([]string{"uid"}, map[string]interface{}{"client_id": []string{clientID}})
	for _, info := range data {
		if uid, ok := info["uid"].(string); ok {
			return uid
		}
	}
	return ""
}

// GetAllGroupIdList 获取所有在线的群组 id 列表
// 对应 PHP Gateway::getAllGroupIdList
func GetAllGroupIdList() []string {
	ensurePool()

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetGroupIDList

	results := pool.queryAllGateways(gd)
	groupMap := make(map[string]bool)

	for _, r := range results {
		if r.err != nil {
			continue
		}
		var groups []string
		if json.Unmarshal(r.data, &groups) != nil {
			continue
		}
		for _, g := range groups {
			groupMap[g] = true
		}
	}

	groupList := make([]string, 0, len(groupMap))
	for g := range groupMap {
		groupList = append(groupList, g)
	}
	return groupList
}

// GetAllGroupUidList 获取所有分组 uid 在线列表
// 对应 PHP Gateway::getAllGroupUidList
func GetAllGroupUidList() map[string][]string {
	data := selectQuery([]string{"uid", "groups"}, nil)
	groupUidMap := make(map[string]map[string]bool)

	for _, info := range data {
		uid, _ := info["uid"].(string)
		if uid == "" {
			continue
		}
		groupsRaw, _ := info["groups"].([]interface{})
		for _, g := range groupsRaw {
			groupStr, _ := g.(string)
			if groupStr == "" {
				continue
			}
			if groupUidMap[groupStr] == nil {
				groupUidMap[groupStr] = make(map[string]bool)
			}
			groupUidMap[groupStr][uid] = true
		}
	}

	result := make(map[string][]string, len(groupUidMap))
	for group, uidSet := range groupUidMap {
		uids := make([]string, 0, len(uidSet))
		for uid := range uidSet {
			uids = append(uids, uid)
		}
		result[group] = uids
	}
	return result
}

// GetAllGroupClientIdList 获取所有群组在线 client_id 列表
// 对应 PHP Gateway::getAllGroupClientIdList
func GetAllGroupClientIdList() map[string][]string {
	data := selectQuery([]string{"groups"}, nil)
	groupClientMap := make(map[string]map[string]bool)

	for clientID, info := range data {
		groupsRaw, _ := info["groups"].([]interface{})
		for _, g := range groupsRaw {
			groupStr, _ := g.(string)
			if groupStr == "" {
				continue
			}
			if groupClientMap[groupStr] == nil {
				groupClientMap[groupStr] = make(map[string]bool)
			}
			groupClientMap[groupStr][clientID] = true
		}
	}

	result := make(map[string][]string, len(groupClientMap))
	for group, clientSet := range groupClientMap {
		clients := make([]string, 0, len(clientSet))
		for cid := range clientSet {
			clients = append(clients, cid)
		}
		result[group] = clients
	}
	return result
}

// ==================== 内部 select 查询 ====================

// selectQuery 根据条件到 gateway 搜索数据
// 对应 PHP Gateway::select
// 返回格式: map[clientID] -> map[field]value
func selectQuery(fields []string, where map[string]interface{}) map[string]map[string]interface{} {
	ensurePool()

	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSelect
	extMap := map[string]interface{}{"fields": fields, "where": where}
	if where == nil {
		extMap["where"] = map[string]interface{}{}
	}
	ext, _ := json.Marshal(extMap)
	gd.ExtData = string(ext)

	results := pool.queryAllGateways(gd)
	allData := make(map[string]map[string]interface{})

	for _, r := range results {
		if r.err != nil {
			continue
		}
		ip, port := parseAddr(r.addr)

		var connData map[uint32]map[string]interface{}
		if json.Unmarshal(r.data, &connData) != nil {
			continue
		}
		for connID, info := range connData {
			clientID := gwctx.AddressToClientID(ip, port, connID)
			allData[clientID] = info
		}
	}
	return allData
}

// formatClientIdFromSelectData 从 select 结果中提取 clientID 列表
func formatClientIdFromSelectData(data map[string]map[string]interface{}) []string {
	list := make([]string, 0, len(data))
	for clientID := range data {
		list = append(list, clientID)
	}
	return list
}

// parseAddr 解析 "ip:port" 字符串为 uint32 IP 和 uint16 Port
func parseAddr(addr string) (uint32, uint16) {
	var a, b, c, d uint32
	var port uint16
	fmt.Sscanf(addr, "%d.%d.%d.%d:%d", &a, &b, &c, &d, &port)
	ip := (a << 24) | (b << 16) | (c << 8) | d
	return ip, port
}
