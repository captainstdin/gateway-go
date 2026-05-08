package gateway_sdk

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	gwctx "adminv.myds.me/a/gatewayworker-go/pkg/context"
	"adminv.myds.me/a/gatewayworker-go/pkg/crypto"
	"adminv.myds.me/a/gatewayworker-go/pkg/protocol"
	"io"
	"net"
	"sync"
	"time"
)

type GatewaySDK struct {
	RegisterAddr []string
	SecretKey    string
	ConnTimeout  time.Duration

	aesKey        []byte
	connPool      map[string]*poolEntry
	mu            sync.Mutex
	addrCache     []string
	addrCacheTime time.Time
}

type poolEntry struct {
	conn      net.Conn
	createdAt time.Time
}

func New(registerAddr []string, secretKey string) *GatewaySDK {
	return &GatewaySDK{
		RegisterAddr: registerAddr,
		SecretKey:    secretKey,
		ConnTimeout:  3 * time.Second,
		aesKey:       crypto.DeriveKey(secretKey),
		connPool:     make(map[string]*poolEntry),
	}
}

func (c *GatewaySDK) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.connPool {
		e.conn.Close()
	}
	c.connPool = make(map[string]*poolEntry)
}

func (c *GatewaySDK) getGatewayAddresses() ([]string, error) {
	// 快速路径：缓存有效直接返回
	c.mu.Lock()
	if time.Since(c.addrCacheTime) < time.Second && len(c.addrCache) > 0 {
		cached := c.addrCache
		c.mu.Unlock()
		return cached, nil
	}
	// 保存旧缓存用于 fallback
	oldCache := c.addrCache
	c.mu.Unlock()

	// 锁外执行网络 I/O，避免阻塞其他 goroutine
	for _, regAddr := range c.RegisterAddr {
		conn, err := net.DialTimeout("tcp", regAddr, c.ConnTimeout)
		if err != nil {
			continue
		}
		msg, _ := json.Marshal(map[string]string{
			"event": "worker_connect", "secret_key": c.SecretKey,
		})
		enc, _ := crypto.EncryptToBase64(msg, c.aesKey)
		conn.Write([]byte(enc + "\n"))
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			line := scanner.Text()
			plaintext, err := crypto.DecryptFromBase64(line, c.aesKey)
			if err == nil {
				var resp struct {
					Addresses []string `json:"addresses"`
				}
				if json.Unmarshal(plaintext, &resp) == nil && len(resp.Addresses) > 0 {
					conn.Close()
					// 写回缓存
					c.mu.Lock()
					c.addrCache = resp.Addresses
					c.addrCacheTime = time.Now()
					c.mu.Unlock()
					return resp.Addresses, nil
				}
			}
		}
		conn.Close()
	}
	if len(oldCache) > 0 {
		return oldCache, nil
	}
	return nil, fmt.Errorf("no gateway addresses available")
}

func (c *GatewaySDK) getConn(addr string) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.connPool[addr]; ok {
		if time.Since(e.createdAt) < 50*time.Second {
			return e.conn, nil
		}
		e.conn.Close()
		delete(c.connPool, addr)
	}

	conn, err := net.DialTimeout("tcp", addr, c.ConnTimeout)
	if err != nil {
		return nil, err
	}
	// Authenticate
	authData := protocol.NewEmptyData()
	authData.Cmd = protocol.CmdGatewayClientConnect
	body, _ := json.Marshal(map[string]string{"secret_key": c.SecretKey})
	authData.Body = body
	authData.Flag = protocol.FlagBodyIsScalar

	encrypted, err := crypto.Encrypt(protocol.Encode(authData), c.aesKey)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("encrypt auth failed: %w", err)
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	if _, err = conn.Write(buf); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write auth failed: %w", err)
	}

	c.connPool[addr] = &poolEntry{conn: conn, createdAt: time.Now()}
	return conn, nil
}

func (c *GatewaySDK) sendToGateway(addr string, gd *protocol.GatewayData) error {
	conn, err := c.getConn(addr)
	if err != nil {
		return err
	}
	data := protocol.Encode(gd)
	encrypted, err := crypto.Encrypt(data, c.aesKey)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	_, err = conn.Write(buf)
	if err != nil {
		c.evictConn(addr)
	}
	return err
}

func (c *GatewaySDK) sendAndRecv(addr string, gd *protocol.GatewayData) ([]byte, error) {
	conn, err := c.getConn(addr)
	if err != nil {
		return nil, err
	}
	data := protocol.Encode(gd)
	encrypted, err := crypto.Encrypt(data, c.aesKey)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	if _, err = conn.Write(buf); err != nil {
		c.evictConn(addr)
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	rLenBuf := make([]byte, 4)
	if _, err = io.ReadFull(conn, rLenBuf); err != nil {
		c.evictConn(addr) // 读失败也要踢出死连接
		return nil, err
	}
	rLen := binary.BigEndian.Uint32(rLenBuf)
	if rLen > protocol.MaxEncryptedPacketSize {
		c.evictConn(addr)
		return nil, fmt.Errorf("response too large: %d bytes", rLen)
	}
	ciphertext := make([]byte, rLen)
	if _, err = io.ReadFull(conn, ciphertext); err != nil {
		c.evictConn(addr)
		return nil, err
	}
	plaintext, err := crypto.Decrypt(ciphertext, c.aesKey)
	if err != nil {
		return nil, err
	}
	// Response format: 4-byte length + data
	if len(plaintext) < 4 {
		return plaintext, nil
	}
	dataLen := binary.BigEndian.Uint32(plaintext[:4])
	if int(dataLen)+4 <= len(plaintext) {
		return plaintext[4 : 4+dataLen], nil
	}
	return plaintext, nil
}

// evictConn 从连接池中移除并关闭连接
func (c *GatewaySDK) evictConn(addr string) {
	c.mu.Lock()
	if e, ok := c.connPool[addr]; ok {
		e.conn.Close()
		delete(c.connPool, addr)
	}
	c.mu.Unlock()
}

func (c *GatewaySDK) sendToAllGateways(gd *protocol.GatewayData) error {
	addrs, err := c.getGatewayAddresses()
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		c.sendToGateway(addr, gd)
	}
	return nil
}

func addrStr(ip uint32, port uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", ip>>24, (ip>>16)&0xff, (ip>>8)&0xff, ip&0xff, port)
}

// ----- Public API -----

func (c *GatewaySDK) SendToClient(clientID string, message []byte) error {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToOne
	gd.ConnectionID = connID
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToGateway(addrStr(ip, port), gd)
}

func (c *GatewaySDK) SendToAll(message []byte) error {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToAll
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToAllGateways(gd)
}

func (c *GatewaySDK) SendToUID(uid interface{}, message []byte) error {
	var uids []interface{}
	switch v := uid.(type) {
	case []interface{}:
		uids = v
	default:
		uids = []interface{}{v}
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToUID
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	ext, _ := json.Marshal(uids)
	gd.ExtData = string(ext)
	return c.sendToAllGateways(gd)
}

func (c *GatewaySDK) SendToGroup(group interface{}, message []byte) error {
	var groups []interface{}
	switch v := group.(type) {
	case []interface{}:
		groups = v
	default:
		groups = []interface{}{v}
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSendToGroup
	gd.Body = message
	gd.Flag = protocol.FlagBodyIsScalar
	ext, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": nil})
	gd.ExtData = string(ext)
	return c.sendToAllGateways(gd)
}

func (c *GatewaySDK) BindUID(clientID, uid string) error {
	return c.sendCmdToClient(clientID, protocol.CmdBindUID, nil, uid)
}

func (c *GatewaySDK) UnbindUID(clientID, uid string) error {
	return c.sendCmdToClient(clientID, protocol.CmdUnbindUID, nil, uid)
}

func (c *GatewaySDK) JoinGroup(clientID, group string) error {
	return c.sendCmdToClient(clientID, protocol.CmdJoinGroup, nil, group)
}

func (c *GatewaySDK) LeaveGroup(clientID, group string) error {
	return c.sendCmdToClient(clientID, protocol.CmdLeaveGroup, nil, group)
}

func (c *GatewaySDK) CloseClient(clientID string, message []byte) error {
	return c.sendCmdToClient(clientID, protocol.CmdKick, message, "")
}

func (c *GatewaySDK) DestroyClient(clientID string) error {
	return c.sendCmdToClient(clientID, protocol.CmdDestroy, nil, "")
}

func (c *GatewaySDK) SetSession(clientID string, session map[string]interface{}) error {
	return c.sendCmdToClient(clientID, protocol.CmdSetSession, nil, gwctx.SessionEncode(session))
}

func (c *GatewaySDK) UpdateSession(clientID string, session map[string]interface{}) error {
	return c.sendCmdToClient(clientID, protocol.CmdUpdateSession, nil, gwctx.SessionEncode(session))
}

func (c *GatewaySDK) IsOnline(clientID string) (bool, error) {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return false, err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdIsOnline
	gd.ConnectionID = connID
	resp, err := c.sendAndRecv(addrStr(ip, port), gd)
	if err != nil {
		return false, err
	}
	var v int
	json.Unmarshal(resp, &v)
	return v == 1, nil
}

func (c *GatewaySDK) GetAllClientCount() (int, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientCountByGroup
	results := c.queryAllGateways(gd)
	total := 0
	for _, r := range results {
		if r.err != nil {
			continue
		}
		var count int
		json.Unmarshal(r.data, &count)
		total += count
	}
	return total, nil
}

func (c *GatewaySDK) sendCmdToClient(clientID string, cmd uint8, body []byte, extData string) error {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = cmd
	gd.ConnectionID = connID
	gd.Body = body
	gd.ExtData = extData
	gd.Flag = protocol.FlagBodyIsScalar
	return c.sendToGateway(addrStr(ip, port), gd)
}

// queryAllGateways 向所有 Gateway 并发查询并收集结果
func (c *GatewaySDK) queryAllGateways(gd *protocol.GatewayData) []queryResult {
	addrs, err := c.getGatewayAddresses()
	if err != nil || len(addrs) == 0 {
		return nil
	}
	results := make([]queryResult, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(idx int, a string) {
			defer wg.Done()
			data, err := c.sendAndRecv(a, gd)
			results[idx] = queryResult{addr: a, data: data, err: err}
		}(i, addr)
	}
	wg.Wait()
	return results
}

type queryResult struct {
	addr string
	data []byte
	err  error
}

// Ungroup 解散分组
func (c *GatewaySDK) Ungroup(group string) error {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdUngroup
	gd.ExtData = group
	return c.sendToAllGateways(gd)
}

// GetSession 获取指定 client_id 的 session
func (c *GatewaySDK) GetSession(clientID string) (map[string]interface{}, error) {
	ip, port, connID, err := gwctx.ClientIDToAddress(clientID)
	if err != nil {
		return nil, err
	}
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetSessionByClientID
	gd.ConnectionID = connID
	resp, err := c.sendAndRecv(addrStr(ip, port), gd)
	if err != nil {
		return nil, err
	}
	var session map[string]interface{}
	json.Unmarshal(resp, &session)
	return session, nil
}

// IsUidOnline 判断 uid 是否在线
func (c *GatewaySDK) IsUidOnline(uid string) (bool, error) {
	clients, err := c.GetClientIdByUid(uid)
	if err != nil {
		return false, err
	}
	return len(clients) > 0, nil
}

// GetClientCountByGroup 获取某个分组的在线连接数
func (c *GatewaySDK) GetClientCountByGroup(group string) (int, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientCountByGroup
	gd.ExtData = group
	results := c.queryAllGateways(gd)
	total := 0
	for _, r := range results {
		if r.err != nil {
			continue
		}
		var count int
		json.Unmarshal(r.data, &count)
		total += count
	}
	return total, nil
}

// GetAllClientSessions 获取所有在线 client 的 session
func (c *GatewaySDK) GetAllClientSessions() (map[string]map[string]interface{}, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetAllClientSessions
	results := c.queryAllGateways(gd)
	all := make(map[string]map[string]interface{})
	for _, r := range results {
		if r.err != nil {
			continue
		}
		ip, port := parseAddr(r.addr)
		var connData map[uint32]string
		if json.Unmarshal(r.data, &connData) != nil {
			continue
		}
		for connID, sessionStr := range connData {
			clientID := gwctx.AddressToClientID(ip, port, connID)
			session := gwctx.SessionDecode(sessionStr)
			if session == nil {
				session = map[string]interface{}{}
			}
			all[clientID] = session
		}
	}
	return all, nil
}

// GetClientSessionsByGroup 获取某个分组成员的 session
func (c *GatewaySDK) GetClientSessionsByGroup(group string) (map[string]map[string]interface{}, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientSessionsByGroup
	gd.ExtData = group
	results := c.queryAllGateways(gd)
	all := make(map[string]map[string]interface{})
	for _, r := range results {
		if r.err != nil {
			continue
		}
		ip, port := parseAddr(r.addr)
		var connData map[uint32]string
		if json.Unmarshal(r.data, &connData) != nil {
			continue
		}
		for connID, sessionStr := range connData {
			clientID := gwctx.AddressToClientID(ip, port, connID)
			session := gwctx.SessionDecode(sessionStr)
			if session == nil {
				session = map[string]interface{}{}
			}
			all[clientID] = session
		}
	}
	return all, nil
}

// GetAllClientIdList 获取所有在线 client_id 列表
func (c *GatewaySDK) GetAllClientIdList() ([]string, error) {
	return c.selectClientIds(nil)
}

// GetClientIdListByGroup 获取某个分组的在线 client_id 列表
func (c *GatewaySDK) GetClientIdListByGroup(group string) ([]string, error) {
	return c.selectClientIds(map[string]interface{}{"groups": []string{group}})
}

// GetClientIdByUid 获取 uid 绑定的 client_id 列表
func (c *GatewaySDK) GetClientIdByUid(uid string) ([]string, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetClientIDByUID
	gd.ExtData = uid
	results := c.queryAllGateways(gd)
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
	return clientList, nil
}

// GetAllGroupIdList 获取所有在线分组 ID 列表
func (c *GatewaySDK) GetAllGroupIdList() ([]string, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdGetGroupIDList
	results := c.queryAllGateways(gd)
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
	list := make([]string, 0, len(groupMap))
	for g := range groupMap {
		list = append(list, g)
	}
	return list, nil
}

// GetUidByClientId 通过 client_id 获取 uid
func (c *GatewaySDK) GetUidByClientId(clientID string) (string, error) {
	data, err := c.selectQuery([]string{"uid"}, map[string]interface{}{"client_id": []string{clientID}})
	if err != nil {
		return "", err
	}
	for _, info := range data {
		if uid, ok := info["uid"].(string); ok {
			return uid, nil
		}
	}
	return "", nil
}

// GetUidListByGroup 获取某个分组在线 uid 列表
func (c *GatewaySDK) GetUidListByGroup(group string) ([]string, error) {
	data, err := c.selectQuery([]string{"uid"}, map[string]interface{}{"groups": []string{group}})
	if err != nil {
		return nil, err
	}
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
	return uids, nil
}

// GetAllUidList 获取全局在线 uid 列表
func (c *GatewaySDK) GetAllUidList() ([]string, error) {
	data, err := c.selectQuery([]string{"uid"}, nil)
	if err != nil {
		return nil, err
	}
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
	return uids, nil
}

// GetAllUidCount 获取全局在线 uid 数量
func (c *GatewaySDK) GetAllUidCount() (int, error) {
	uids, err := c.GetAllUidList()
	return len(uids), err
}

// GetUidCountByGroup 获取分组在线 uid 数量
func (c *GatewaySDK) GetUidCountByGroup(group string) (int, error) {
	uids, err := c.GetUidListByGroup(group)
	return len(uids), err
}

// ----- 内部 select 查询 -----

func (c *GatewaySDK) selectQuery(fields []string, where map[string]interface{}) (map[string]map[string]interface{}, error) {
	gd := protocol.NewEmptyData()
	gd.Cmd = protocol.CmdSelect
	extMap := map[string]interface{}{"fields": fields, "where": where}
	if where == nil {
		extMap["where"] = map[string]interface{}{}
	}
	ext, _ := json.Marshal(extMap)
	gd.ExtData = string(ext)

	results := c.queryAllGateways(gd)
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
	return allData, nil
}

func (c *GatewaySDK) selectClientIds(where map[string]interface{}) ([]string, error) {
	data, err := c.selectQuery([]string{"uid"}, where)
	if err != nil {
		return nil, err
	}
	list := make([]string, 0, len(data))
	for clientID := range data {
		list = append(list, clientID)
	}
	return list, nil
}

func parseAddr(addr string) (uint32, uint16) {
	var a, b, c, d uint32
	var port uint16
	fmt.Sscanf(addr, "%d.%d.%d.%d:%d", &a, &b, &c, &d, &port)
	ip := (a << 24) | (b << 16) | (c << 8) | d
	return ip, port
}

