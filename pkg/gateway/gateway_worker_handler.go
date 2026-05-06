package gateway

import (
	"encoding/binary"
	"encoding/json"
	gwctx "gatewayworker-go/pkg/context"
	"gatewayworker-go/pkg/protocol"
	"log"
	"net"
)

// handleWorkerMessage 处理 Worker 发来的命令，返回 workerKey
func (g *Gateway) handleWorkerMessage(conn net.Conn, data *protocol.GatewayData, authorized *bool, workerKey string) string {
	switch data.Cmd {

	case protocol.CmdWorkerConnect:
		var info struct {
			WorkerKey string `json:"worker_key"`
			SecretKey string `json:"secret_key"`
		}
		json.Unmarshal(data.Body, &info)
		if info.SecretKey != g.SecretKey {
			log.Printf("[Gateway] Worker key mismatch from %s", conn.RemoteAddr())
			return "__close__"
		}
		key := conn.RemoteAddr().(*net.TCPAddr).IP.String() + ":" + info.WorkerKey
		g.mu.Lock()
		if old, ok := g.workerConns[key]; ok {
			old.Close()
		}
		g.workerConns[key] = conn
		g.mu.Unlock()
		*authorized = true
		g.router.OnWorkerConnected(key)
		log.Printf("[Gateway] Worker connected: %s", key)
		return key

	case protocol.CmdGatewayClientConnect:
		var info struct {
			SecretKey string `json:"secret_key"`
		}
		json.Unmarshal(data.Body, &info)
		if info.SecretKey != g.SecretKey {
			log.Printf("[Gateway] GatewayClient key mismatch")
			return "__close__"
		}
		*authorized = true
		return workerKey

	case protocol.CmdSendToOne:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		if cc != nil {
			cc.Conn.Write(data.Body)
		}

	case protocol.CmdKick:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		if cc != nil {
			if len(data.Body) > 0 {
				cc.Conn.Write(data.Body)
			}
			cc.Conn.Close()
		}

	case protocol.CmdDestroy:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		if cc != nil {
			cc.Conn.Close()
		}

	case protocol.CmdSendToAll:
		g.handleSendToAll(data)

	case protocol.CmdBindUID:
		g.handleBindUID(data)

	case protocol.CmdUnbindUID:
		g.handleUnbindUID(data)

	case protocol.CmdSendToUID:
		g.handleSendToUID(data)

	case protocol.CmdJoinGroup:
		g.handleJoinGroup(data)

	case protocol.CmdLeaveGroup:
		g.handleLeaveGroup(data)

	case protocol.CmdUngroup:
		g.handleUngroup(data)

	case protocol.CmdSendToGroup:
		g.handleSendToGroup(data)

	case protocol.CmdSetSession:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		if cc != nil {
			cc.Session = data.ExtData
		}

	case protocol.CmdUpdateSession:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		if cc != nil {
			if cc.Session == "" {
				cc.Session = data.ExtData
			} else {
				old := gwctx.SessionDecode(cc.Session)
				merge := gwctx.SessionDecode(data.ExtData)
				for k, v := range merge {
					old[k] = v
				}
				cc.Session = gwctx.SessionEncode(old)
			}
		}

	case protocol.CmdGetSessionByClientID:
		g.mu.RLock()
		cc := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		var session string
		if cc == nil {
			session = "null"
		} else if cc.Session == "" {
			session = "{}"
		} else {
			session = cc.Session
		}
		g.sendQueryResponse(conn, []byte(session))

	case protocol.CmdGetAllClientSessions:
		result := make(map[uint32]string)
		g.mu.RLock()
		for id, cc := range g.clientConns {
			result[id] = cc.Session
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(result)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdIsOnline:
		g.mu.RLock()
		_, exists := g.clientConns[data.ConnectionID]
		g.mu.RUnlock()
		v := 0
		if exists {
			v = 1
		}
		buf, _ := json.Marshal(v)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdGetClientCountByGroup:
		group := data.ExtData
		g.mu.RLock()
		var count int
		if group != "" {
			if gm, ok := g.groupConns[group]; ok {
				count = len(gm)
			}
		} else {
			count = len(g.clientConns)
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(count)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdGetClientIDByUID:
		uid := data.ExtData
		g.mu.RLock()
		var ids []uint32
		if uidMap, ok := g.uidConns[uid]; ok {
			for id := range uidMap {
				ids = append(ids, id)
			}
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(ids)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdBatchGetClientIDByUID:
		var uids []string
		json.Unmarshal([]byte(data.ExtData), &uids)
		result := make(map[string][]uint32)
		g.mu.RLock()
		for _, uid := range uids {
			if uidMap, ok := g.uidConns[uid]; ok {
				for id := range uidMap {
					result[uid] = append(result[uid], id)
				}
			} else {
				result[uid] = []uint32{}
			}
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(result)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdGetGroupIDList:
		g.mu.RLock()
		groups := make([]string, 0, len(g.groupConns))
		for group := range g.groupConns {
			groups = append(groups, group)
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(groups)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdGetClientSessionsByGroup:
		group := data.ExtData
		result := make(map[uint32]string)
		g.mu.RLock()
		if gm, ok := g.groupConns[group]; ok {
			for id, cc := range gm {
				result[id] = cc.Session
			}
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(result)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdBatchGetClientCountByGroup:
		var groups []string
		json.Unmarshal([]byte(data.ExtData), &groups)
		result := make(map[string]int)
		g.mu.RLock()
		for _, group := range groups {
			if gm, ok := g.groupConns[group]; ok {
				result[group] = len(gm)
			} else {
				result[group] = 0
			}
		}
		g.mu.RUnlock()
		buf, _ := json.Marshal(result)
		g.sendQueryResponse(conn, buf)

	case protocol.CmdSelect:
		g.handleSelect(conn, data)

	case protocol.CmdPing:
		// ignore

	default:
		log.Printf("[Gateway] Unknown cmd: %d", data.Cmd)
	}

	return workerKey
}

func (g *Gateway) sendQueryResponse(conn net.Conn, data []byte) {
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(data)))
	copy(buf[4:], data)
	g.sendEncrypted(conn, buf)
}


func (g *Gateway) handleBindUID(data *protocol.GatewayData) {
	uid := data.ExtData
	if uid == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cc, ok := g.clientConns[data.ConnectionID]
	if !ok {
		return
	}
	if cc.UID != "" {
		if uidMap, ok := g.uidConns[cc.UID]; ok {
			delete(uidMap, cc.ID)
			if len(uidMap) == 0 {
				delete(g.uidConns, cc.UID)
			}
		}
	}
	cc.UID = uid
	if g.uidConns[uid] == nil {
		g.uidConns[uid] = make(map[uint32]*ClientConnection)
	}
	g.uidConns[uid][cc.ID] = cc
}

func (g *Gateway) handleUnbindUID(data *protocol.GatewayData) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cc, ok := g.clientConns[data.ConnectionID]
	if !ok || cc.UID == "" {
		return
	}
	if uidMap, ok := g.uidConns[cc.UID]; ok {
		delete(uidMap, cc.ID)
		if len(uidMap) == 0 {
			delete(g.uidConns, cc.UID)
		}
	}
	cc.UID = ""
}

func (g *Gateway) handleSendToUID(data *protocol.GatewayData) {
	var uids []string
	json.Unmarshal([]byte(data.ExtData), &uids)

	var targets []ClientConn
	g.mu.RLock()
	for _, uid := range uids {
		if uidMap, ok := g.uidConns[uid]; ok {
			for _, cc := range uidMap {
				targets = append(targets, cc.Conn)
			}
		}
	}
	g.mu.RUnlock()

	for _, conn := range targets {
		conn.Write(data.Body)
	}
}

func (g *Gateway) handleJoinGroup(data *protocol.GatewayData) {
	group := data.ExtData
	if group == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cc, ok := g.clientConns[data.ConnectionID]
	if !ok {
		return
	}
	cc.Groups[group] = true
	if g.groupConns[group] == nil {
		g.groupConns[group] = make(map[uint32]*ClientConnection)
	}
	g.groupConns[group][cc.ID] = cc
}

func (g *Gateway) handleLeaveGroup(data *protocol.GatewayData) {
	group := data.ExtData
	if group == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cc, ok := g.clientConns[data.ConnectionID]
	if !ok {
		return
	}
	delete(cc.Groups, group)
	if gm, ok := g.groupConns[group]; ok {
		delete(gm, cc.ID)
		if len(gm) == 0 {
			delete(g.groupConns, group)
		}
	}
}

func (g *Gateway) handleUngroup(data *protocol.GatewayData) {
	group := data.ExtData
	if group == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if gm, ok := g.groupConns[group]; ok {
		for _, cc := range gm {
			delete(cc.Groups, group)
		}
		delete(g.groupConns, group)
	}
}

func (g *Gateway) handleSendToGroup(data *protocol.GatewayData) {
	var ext struct {
		Group   []string          `json:"group"`
		Exclude map[uint32]uint32 `json:"exclude"`
	}
	json.Unmarshal([]byte(data.ExtData), &ext)

	var targets []ClientConn
	g.mu.RLock()
	for _, group := range ext.Group {
		if gm, ok := g.groupConns[group]; ok {
			for _, cc := range gm {
				if _, excluded := ext.Exclude[cc.ID]; !excluded {
					targets = append(targets, cc.Conn)
				}
			}
		}
	}
	g.mu.RUnlock()

	for _, conn := range targets {
		conn.Write(data.Body)
	}
}

func (g *Gateway) handleSelect(conn net.Conn, data *protocol.GatewayData) {
	var ext struct {
		Fields []string                   `json:"fields"`
		Where  map[string]json.RawMessage `json:"where"`
	}
	json.Unmarshal([]byte(data.ExtData), &ext)

	result := make(map[uint32]map[string]interface{})
	g.mu.RLock()

	if connIDs, ok := ext.Where["connection_id"]; ok {
		var ids []uint32
		json.Unmarshal(connIDs, &ids)
		for _, id := range ids {
			if cc, ok := g.clientConns[id]; ok {
				result[id] = g.buildFieldMap(cc, ext.Fields)
			}
		}
	}
	if groupsRaw, ok := ext.Where["groups"]; ok {
		var groups []string
		json.Unmarshal(groupsRaw, &groups)
		for _, group := range groups {
			if gm, ok := g.groupConns[group]; ok {
				for id, cc := range gm {
					if _, exists := result[id]; !exists {
						result[id] = g.buildFieldMap(cc, ext.Fields)
					}
				}
			}
		}
	}
	if uidsRaw, ok := ext.Where["uid"]; ok {
		var uids []string
		json.Unmarshal(uidsRaw, &uids)
		for _, uid := range uids {
			if uidMap, ok := g.uidConns[uid]; ok {
				for id, cc := range uidMap {
					if _, exists := result[id]; !exists {
						result[id] = g.buildFieldMap(cc, ext.Fields)
					}
				}
			}
		}
	}
	if len(ext.Where) == 0 {
		for id, cc := range g.clientConns {
			result[id] = g.buildFieldMap(cc, ext.Fields)
		}
	}
	g.mu.RUnlock()

	buf, _ := json.Marshal(result)
	g.sendQueryResponse(conn, buf)
}

func (g *Gateway) buildFieldMap(cc *ClientConnection, fields []string) map[string]interface{} {
	m := make(map[string]interface{})
	for _, f := range fields {
		switch f {
		case "uid":
			m["uid"] = cc.UID
		case "groups":
			groups := make([]string, 0, len(cc.Groups))
			for g := range cc.Groups {
				groups = append(groups, g)
			}
			m["groups"] = groups
		case "session":
			m["session"] = cc.Session
		}
	}
	return m
}
