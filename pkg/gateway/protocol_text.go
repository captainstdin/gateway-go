package gateway

import "bytes"

// TextProtocol text 文本协议（与 PHP Workerman 的 text 协议完全一致）
//
// 包格式: 数据 + 换行符 "\n"
//
// 每个数据包以换行符 "\n" 结尾。
// 适合纯文本通讯、telnet 调试、与硬件或 App 的简单文本交互。
//
// 用法: gateway -listen "text://0.0.0.0:7273"
//
// telnet 测试:
//
//	telnet 127.0.0.1 7273
//	hello        <- 输入后回车
type TextProtocol struct{}

// Input 查找换行符 "\n" 作为包分隔
func (p *TextProtocol) Input(buf []byte) int {
	pos := bytes.IndexByte(buf, '\n')
	if pos < 0 {
		// 防止缓冲区无限增长（无换行符时限制最大 10MB）
		if len(buf) > maxPacketSize {
			return -1
		}
		return 0
	}
	return pos + 1
}

// Decode 去掉末尾换行符，返回纯文本数据
func (p *TextProtocol) Decode(buf []byte) []byte {
	return bytes.TrimRight(buf, "\r\n")
}

// Encode 在数据末尾追加换行符
func (p *TextProtocol) Encode(data []byte) []byte {
	return append(data, '\n')
}
