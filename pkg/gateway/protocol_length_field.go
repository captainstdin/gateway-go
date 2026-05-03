package gateway

// LengthFieldProtocol 4字节大端长度头 + body 协议
//
// 包格式: [4字节 body长度(Big-Endian)] [body...]
//
// 这是 GatewayWorker-Go 的默认 TCP 协议（tcp://）。
//
// 注意：此协议的4字节头部存储的是 body 长度，不包含头部自身。
// 这与 PHP Workerman 的 frame 协议不同（frame 存储的是总包长，包含4字节头部）。
// 如需与 PHP frame 协议兼容，请使用 FrameProtocol（frame://）。
//
// 最大包体限制为 10MB。
type LengthFieldProtocol struct{}

const maxPacketSize = 10 * 1024 * 1024 // 10MB

// Input 从缓冲区前4字节读取 body 长度，返回整包长度 (4 + bodyLen)
func (p *LengthFieldProtocol) Input(buf []byte) int {
	if len(buf) < 4 {
		return 0
	}
	bodyLen := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if bodyLen < 0 || bodyLen > maxPacketSize {
		return -1 // 协议错误
	}
	return 4 + bodyLen
}

// Decode 去掉4字节头部，返回 body
func (p *LengthFieldProtocol) Decode(buf []byte) []byte {
	if len(buf) <= 4 {
		return []byte{}
	}
	return buf[4:]
}

// Encode 在 data 前加上4字节大端长度头
func (p *LengthFieldProtocol) Encode(data []byte) []byte {
	length := uint32(len(data))
	header := []byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)}
	return append(header, data...)
}
