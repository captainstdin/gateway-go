package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 命令字常量 —— Gateway 与 Worker 间通讯
const (
	// Gateway → Worker: 事件通知
	CmdOnConnect           uint8 = 1   // 新客户端连接
	CmdOnMessage           uint8 = 3   // 客户端消息
	CmdOnClose             uint8 = 4   // 客户端断开
	CmdOnWebSocketConnect  uint8 = 205 // WebSocket 握手完成

	// Worker → Gateway: 指令
	CmdSendToOne           uint8 = 5   // 发给单个客户端
	CmdSendToAll           uint8 = 6   // 广播
	CmdKick                uint8 = 7   // 踢出客户端（先发消息再关闭）
	CmdDestroy             uint8 = 8   // 直接销毁连接
	CmdUpdateSession       uint8 = 9   // 合并 session
	CmdGetAllClientSessions uint8 = 10 // 获取所有客户端 session
	CmdIsOnline            uint8 = 11  // 判断是否在线
	CmdBindUID             uint8 = 12  // 绑定 UID
	CmdUnbindUID           uint8 = 13  // 解绑 UID
	CmdSendToUID           uint8 = 14  // 向 UID 发送消息
	CmdGetClientIDByUID    uint8 = 15  // 获取 UID 绑定的 client_id
	CmdBatchGetClientIDByUID uint8 = 16 // 批量获取
	CmdJoinGroup           uint8 = 20  // 加入 group
	CmdLeaveGroup          uint8 = 21  // 离开 group
	CmdSendToGroup         uint8 = 22  // 向 group 发送消息
	CmdGetClientSessionsByGroup uint8 = 23 // 获取 group 成员 session
	CmdGetClientCountByGroup uint8 = 24 // 获取 group 在线数
	CmdSelect              uint8 = 25  // 按条件查询
	CmdGetGroupIDList      uint8 = 26  // 获取在线 group 列表
	CmdUngroup             uint8 = 27  // 解散 group
	CmdBatchGetClientCountByGroup uint8 = 28 // 批量获取 group 在线数

	// 内部认证 & 心跳
	CmdWorkerConnect       uint8 = 200 // Worker 连接 Gateway 认证
	CmdPing                uint8 = 201 // 心跳
	CmdGatewayClientConnect uint8 = 202 // GatewayClient 连接 Gateway 认证
	CmdGetSessionByClientID uint8 = 203 // 获取指定 client 的 session
	CmdSetSession          uint8 = 204 // 覆盖 session
)

// 标志位
const (
	FlagBodyIsScalar  uint8 = 0x01 // body 是标量（原始字符串）
	FlagNotCallEncode uint8 = 0x02 // 发送时不调用协议 encode
)

// HeadLen 包头固定长度 28 字节
const HeadLen = 28

// MaxEncryptedPacketSize 内部加密通讯的最大包体限制（50MB）
// 用于 Gateway、Worker、GatewayClient 读取加密包时的安全检查，
// 防止恶意或损坏的数据导致 OOM。
const MaxEncryptedPacketSize = 50 * 1024 * 1024

// GatewayData Gateway 与 Worker 间通讯的数据结构
type GatewayData struct {
	PackLen      uint32 // 整包长度（编码时自动计算）
	Cmd          uint8  // 命令字
	LocalIP      uint32 // Gateway 内网 IP
	LocalPort    uint16 // Gateway 内部通讯端口
	ClientIP     uint32 // 客户端 IP
	ClientPort   uint16 // 客户端端口
	ConnectionID uint32 // 连接 ID
	Flag         uint8  // 标志位
	GatewayPort  uint16 // Gateway 对外监听端口
	ExtLen       uint32 // ExtData 长度（编码时自动计算）
	ExtData      string // 扩展数据（session 等）
	Body         []byte // 包体
}

// NewEmptyData 创建一个空的 GatewayData
func NewEmptyData() *GatewayData {
	return &GatewayData{}
}

// Encode 将 GatewayData 编码为二进制
// 包格式: [4B pack_len][1B cmd][4B local_ip][2B local_port][4B client_ip]
//
//	[2B client_port][4B connection_id][1B flag][2B gateway_port]
//	[4B ext_len][ext_data...][body...]
func Encode(data *GatewayData) []byte {
	extData := []byte(data.ExtData)
	extLen := uint32(len(extData))
	bodyLen := uint32(len(data.Body))
	packLen := uint32(HeadLen) + extLen + bodyLen

	buf := make([]byte, packLen)

	// 写入头部（28 字节，Big-Endian）
	binary.BigEndian.PutUint32(buf[0:4], packLen)           // pack_len
	buf[4] = data.Cmd                                        // cmd
	binary.BigEndian.PutUint32(buf[5:9], data.LocalIP)      // local_ip
	binary.BigEndian.PutUint16(buf[9:11], data.LocalPort)   // local_port
	binary.BigEndian.PutUint32(buf[11:15], data.ClientIP)   // client_ip
	binary.BigEndian.PutUint16(buf[15:17], data.ClientPort) // client_port
	binary.BigEndian.PutUint32(buf[17:21], data.ConnectionID) // connection_id
	buf[21] = data.Flag                                       // flag
	binary.BigEndian.PutUint16(buf[22:24], data.GatewayPort) // gateway_port
	binary.BigEndian.PutUint32(buf[24:28], extLen)           // ext_len

	// 写入变长部分
	if extLen > 0 {
		copy(buf[HeadLen:], extData)
	}
	if bodyLen > 0 {
		copy(buf[HeadLen+extLen:], data.Body)
	}

	return buf
}

// Decode 将二进制数据解码为 GatewayData
func Decode(buf []byte) (*GatewayData, error) {
	if len(buf) < HeadLen {
		return nil, fmt.Errorf("buffer too short: %d < %d", len(buf), HeadLen)
	}

	data := &GatewayData{}
	data.PackLen = binary.BigEndian.Uint32(buf[0:4])
	data.Cmd = buf[4]
	data.LocalIP = binary.BigEndian.Uint32(buf[5:9])
	data.LocalPort = binary.BigEndian.Uint16(buf[9:11])
	data.ClientIP = binary.BigEndian.Uint32(buf[11:15])
	data.ClientPort = binary.BigEndian.Uint16(buf[15:17])
	data.ConnectionID = binary.BigEndian.Uint32(buf[17:21])
	data.Flag = buf[21]
	data.GatewayPort = binary.BigEndian.Uint16(buf[22:24])
	data.ExtLen = binary.BigEndian.Uint32(buf[24:28])

	if uint32(len(buf)) < uint32(HeadLen)+data.ExtLen {
		return nil, fmt.Errorf("buffer too short for ext_data: need %d, got %d",
			HeadLen+int(data.ExtLen), len(buf))
	}

	if data.ExtLen > 0 {
		data.ExtData = string(buf[HeadLen : HeadLen+data.ExtLen])
	}

	bodyStart := uint32(HeadLen) + data.ExtLen
	if uint32(len(buf)) > bodyStart {
		data.Body = buf[bodyStart:]
	}

	return data, nil
}

// Input 从 TCP 流的前4字节读取 pack_len，用于粘包处理
// 返回0表示数据不够，需要继续读取
func Input(buf []byte) (uint32, error) {
	if len(buf) < 4 {
		return 0, nil
	}
	packLen := binary.BigEndian.Uint32(buf[0:4])
	if packLen < HeadLen {
		return 0, errors.New("invalid pack_len: too small")
	}
	return packLen, nil
}
