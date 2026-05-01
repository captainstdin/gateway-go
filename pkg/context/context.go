package context

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Context 请求上下文，在 Worker 处理每个 Gateway 转发的请求时设置
type Context struct {
	LocalIP      uint32                 // Gateway 内网 IP
	LocalPort    uint16                 // Gateway 内部通讯端口
	ClientIP     uint32                 // 客户端 IP
	ClientPort   uint16                 // 客户端端口
	ClientID     string                 // 20 字符 hex，全局唯一客户端标识
	ConnectionID uint32                 // 连接 ID
	Session      map[string]interface{} // 当前 session
	OldSession   map[string]interface{} // 变更前的 session（用于检测是否修改）
}

// AddressToClientID 将 (localIP, localPort, connectionID) 编码为 20 字符 hex 字符串
// 编码规则与 PHP 版保持一致：pack('NnN', localIP, localPort, connectionID) -> bin2hex
func AddressToClientID(localIP uint32, localPort uint16, connectionID uint32) string {
	buf := make([]byte, 10)
	binary.BigEndian.PutUint32(buf[0:4], localIP)
	binary.BigEndian.PutUint16(buf[4:6], localPort)
	binary.BigEndian.PutUint32(buf[6:10], connectionID)
	return hex.EncodeToString(buf)
}

// ClientIDToAddress 将 20 字符 hex 字符串解码为 (localIP, localPort, connectionID)
func ClientIDToAddress(clientID string) (localIP uint32, localPort uint16, connectionID uint32, err error) {
	if len(clientID) != 20 {
		err = fmt.Errorf("invalid client_id length: %d, expected 20", len(clientID))
		return
	}
	buf, err := hex.DecodeString(clientID)
	if err != nil {
		err = fmt.Errorf("invalid client_id hex: %w", err)
		return
	}
	if len(buf) != 10 {
		err = errors.New("invalid client_id: decoded length mismatch")
		return
	}
	localIP = binary.BigEndian.Uint32(buf[0:4])
	localPort = binary.BigEndian.Uint16(buf[4:6])
	connectionID = binary.BigEndian.Uint32(buf[6:10])
	return
}

// SessionEncode 将 session map 编码为 JSON 字符串
func SessionEncode(session map[string]interface{}) string {
	if session == nil || len(session) == 0 {
		return ""
	}
	data, err := json.Marshal(session)
	if err != nil {
		return ""
	}
	return string(data)
}

// SessionDecode 将 JSON 字符串解码为 session map
func SessionDecode(data string) map[string]interface{} {
	if data == "" {
		return nil
	}
	var session map[string]interface{}
	if err := json.Unmarshal([]byte(data), &session); err != nil {
		return nil
	}
	return session
}

// Clear 清除上下文
func (c *Context) Clear() {
	c.LocalIP = 0
	c.LocalPort = 0
	c.ClientIP = 0
	c.ClientPort = 0
	c.ClientID = ""
	c.ConnectionID = 0
	c.Session = nil
	c.OldSession = nil
}
