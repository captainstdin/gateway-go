package gateway_api

import (
	"encoding/json"
	"fmt"
	gwctx "gatewayworker-go/pkg/context"
	"gatewayworker-go/pkg/protocol"
	"gatewayworker-go/pkg/worker"
)

var bw *worker.BusinessWorker

func SetBusinessWorker(w *worker.BusinessWorker) { bw = w }

func sendCmd(clientID string, cmd uint8, message []byte, extData string) {
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

func sendCmdToAll(cmd uint8, message []byte, extData string) {
	gd := protocol.NewEmptyData()
	gd.Cmd = cmd
	gd.Body = message
	gd.ExtData = extData
	gd.Flag = protocol.FlagBodyIsScalar
	bw.SendToAllGateways(protocol.Encode(gd))
}

func addrStr(ip uint32, port uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", ip>>24, (ip>>16)&0xff, (ip>>8)&0xff, ip&0xff, port)
}

func SendToClient(clientID string, message []byte) {
	sendCmd(clientID, protocol.CmdSendToOne, message, "")
}

func SendToAll(message []byte, clientIDs, excludeIDs []string) {
	if len(clientIDs) > 0 {
		grouped := make(map[string][]uint32)
		for _, cid := range clientIDs {
			ip, port, connID, err := gwctx.ClientIDToAddress(cid)
			if err != nil {
				continue
			}
			grouped[addrStr(ip, port)] = append(grouped[addrStr(ip, port)], connID)
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
	sendCmdToAll(protocol.CmdSendToAll, message, "")
}

func BindUID(clientID, uid string)     { sendCmd(clientID, protocol.CmdBindUID, nil, uid) }
func UnbindUID(clientID, uid string)   { sendCmd(clientID, protocol.CmdUnbindUID, nil, uid) }
func JoinGroup(clientID, group string) { sendCmd(clientID, protocol.CmdJoinGroup, nil, group) }
func LeaveGroup(clientID, group string) { sendCmd(clientID, protocol.CmdLeaveGroup, nil, group) }

func SendToUID(uid interface{}, message []byte) {
	var uids []interface{}
	switch v := uid.(type) {
	case []interface{}:
		uids = v
	default:
		uids = []interface{}{v}
	}
	ext, _ := json.Marshal(uids)
	sendCmdToAll(protocol.CmdSendToUID, message, string(ext))
}

func SendToGroup(group interface{}, message []byte) {
	var groups []interface{}
	switch v := group.(type) {
	case []interface{}:
		groups = v
	default:
		groups = []interface{}{v}
	}
	ext, _ := json.Marshal(map[string]interface{}{"group": groups, "exclude": nil})
	sendCmdToAll(protocol.CmdSendToGroup, message, string(ext))
}

func Ungroup(group string) { sendCmdToAll(protocol.CmdUngroup, nil, group) }

func SetSession(clientID string, session map[string]interface{}) {
	sendCmd(clientID, protocol.CmdSetSession, nil, gwctx.SessionEncode(session))
}

func UpdateSession(clientID string, session map[string]interface{}) {
	sendCmd(clientID, protocol.CmdUpdateSession, nil, gwctx.SessionEncode(session))
}

func CloseClient(clientID string, message []byte) {
	sendCmd(clientID, protocol.CmdKick, message, "")
}

func DestroyClient(clientID string) {
	sendCmd(clientID, protocol.CmdDestroy, nil, "")
}
