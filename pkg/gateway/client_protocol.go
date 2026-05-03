package gateway

import "strings"

// ClientProtocol 客户端通讯协议接口（面向外部 TCP 客户端）
//
// 参考 PHP Workerman 的 ProtocolInterface，实现三个方法即可自定义协议。
// 协议负责解决 TCP 粘包/半包问题，以及数据的编解码。
//
// 使用方式：
//  1. 实现 ClientProtocol 接口
//  2. 调用 RegisterProtocol("myproto", &MyProtocol{}) 注册
//  3. Gateway 启动时使用 -listen "myproto://0.0.0.0:7273"
type ClientProtocol interface {
	// Input 从 TCP 流缓冲区中判断一个完整包的长度。
	//  返回值 > 0: 表示完整包的总长度（包含头部），框架会等凑齐后调用 Decode
	//  返回值 == 0: 数据不够，需要继续等待更多数据
	//  返回值 < 0: 协议错误，连接将被关闭
	Input(buf []byte) int

	// Decode 将一个完整的原始数据包解码为业务数据。
	// 参数 buf 的长度等于 Input 返回的值。
	Decode(buf []byte) []byte

	// Encode 将业务数据编码为协议格式的字节，用于发送给客户端。
	Encode(data []byte) []byte
}

// protocolRegistry 内置协议注册表
var protocolRegistry = map[string]ClientProtocol{}

// RegisterProtocol 注册自定义协议，name 将作为 URI 前缀使用（大小写不敏感）。
//
// 例如:
//
//	gateway.RegisterProtocol("jsonNL", &JsonNLProtocol{})
//
// 然后启动时使用:
//
//	-listen "jsonNL://0.0.0.0:7273"
func RegisterProtocol(name string, p ClientProtocol) {
	protocolRegistry[strings.ToLower(name)] = p
}

// getProtocol 按名称查找已注册的协议（大小写不敏感）
func getProtocol(name string) (ClientProtocol, bool) {
	p, ok := protocolRegistry[strings.ToLower(name)]
	return p, ok
}

func init() {
	// 注册内置协议（与 PHP Workerman 一致）
	RegisterProtocol("tcp", &LengthFieldProtocol{})  // 4字节 body 长度头（Go 版默认 TCP 协议）
	RegisterProtocol("frame", &FrameProtocol{})      // 4字节总包长头（与 PHP Workerman frame 协议一致）
	RegisterProtocol("text", &TextProtocol{})        // 换行符分隔文本协议（与 PHP Workerman text 协议一致）
}
