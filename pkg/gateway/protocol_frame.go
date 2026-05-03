package gateway

// FrameProtocol frame 二进制帧协议（与 PHP Workerman 的 frame 协议完全一致）
//
// 包格式: [4字节 总包长(Big-Endian, 包含这4字节自身)] [body...]
//
// 注意：PHP Workerman 的 frame 协议中，4字节头部存储的是"总包长"（即 4 + body长度），
// 而不是单纯的 body 长度。这与 GatewayWorker-Go 默认的 tcp 协议（LengthFieldProtocol）不同。
//
// 用法: gateway -listen "frame://0.0.0.0:7273"
type FrameProtocol struct{}

// Input 从缓冲区前4字节读取总包长（包含自身4字节），返回总包长
func (p *FrameProtocol) Input(buf []byte) int {
	if len(buf) < 4 {
		return 0
	}
	totalLen := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if totalLen < 4 || totalLen > maxPacketSize {
		return -1 // 协议错误
	}
	return totalLen
}

// Decode 去掉4字节头部，返回 body
func (p *FrameProtocol) Decode(buf []byte) []byte {
	if len(buf) <= 4 {
		return []byte{}
	}
	return buf[4:]
}

// Encode 在 data 前加上4字节大端"总包长"头（总包长 = 4 + bodyLen）
func (p *FrameProtocol) Encode(data []byte) []byte {
	totalLength := uint32(4 + len(data))
	header := []byte{byte(totalLength >> 24), byte(totalLength >> 16), byte(totalLength >> 8), byte(totalLength)}
	return append(header, data...)
}
