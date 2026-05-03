// gateway-edit 是一个示例程序，展示如何使用自定义 TCP 协议启动 Gateway。
//
// 此示例注册了一个 JsonNL 协议（以换行符 \n 分隔的 JSON 文本协议），
// 与 PHP Workerman 的自定义协议用法对标。
//
// 构建:
//
//	go build -o bin/gateway-edit cmd/gateway-edit/main.go
//
// 使用:
//
//	./gateway-edit -listen "jsonNL://0.0.0.0:7273" -key "your-secret"
//	./gateway-edit -listen "websocket://0.0.0.0:7272,jsonNL://0.0.0.0:7273" -key "your-secret"
//	./gateway-edit -listen "text://0.0.0.0:7273" -key "your-secret"
//
// 测试（telnet）:
//
//	telnet 127.0.0.1 7273
//	{"type":"chat","msg":"hello"}   <- 输入后回车
package main

import (
	"bytes"
	"flag"
	"gatewayworker-go/pkg/gateway"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// =====================================================
// 自定义协议示例：JsonNL（以 \n 分隔的 JSON 文本协议）
// 对标 PHP Workerman 的 Protocols\JsonNL
// =====================================================

// JsonNLProtocol 以换行符 \n 作为包边界的文本协议。
//
// 包格式: JSON数据 + "\n"
//
// PHP 版对照:
//
//	Protocols\JsonNL::input($buffer)  → Input(buf)
//	Protocols\JsonNL::decode($buffer) → Decode(buf)
//	Protocols\JsonNL::encode($buffer) → Encode(data)
type JsonNLProtocol struct{}

// Input 查找换行符 \n，返回完整包的长度（含换行符）。
//
// PHP 对照: strpos($buffer, "\n") === false ? 0 : $pos+1
func (p *JsonNLProtocol) Input(buf []byte) int {
	pos := bytes.IndexByte(buf, '\n')
	if pos < 0 {
		// 没有换行符，继续等待（但限制缓冲区不超过 10MB）
		if len(buf) > 10*1024*1024 {
			return -1
		}
		return 0
	}
	return pos + 1
}

// Decode 去掉末尾的换行符，返回纯 JSON 数据。
//
// PHP 对照: json_decode(trim($buffer), true)
// 注意：Go 版不在协议层做 json_decode，JSON 解析放在 OnMessage 业务回调中。
func (p *JsonNLProtocol) Decode(buf []byte) []byte {
	return bytes.TrimRight(buf, "\r\n")
}

// Encode 在数据末尾追加换行符。
//
// PHP 对照: json_encode($buffer)."\n"
// 注意：Go 版不在协议层做 json_encode，JSON 序列化放在业务层。
func (p *JsonNLProtocol) Encode(data []byte) []byte {
	return append(data, '\n')
}

func main() {
	listen := flag.String("listen", "jsonNL://0.0.0.0:7273", "监听地址，逗号分隔多个。支持: websocket:// ws:// tcp:// frame:// text:// jsonNL:// 或其他已注册协议")
	lanIP := flag.String("lan-ip", "127.0.0.1", "内网通讯 IP")
	startPort := flag.Int("start-port", 54321, "内部通讯起始端口")
	instanceID := flag.Int("id", 0, "实例 ID")
	registerAddr := flag.String("register", "127.0.0.1:51234", "Register 地址，逗号分隔多个")
	secretKey := flag.String("key", "", "认证密钥")
	pingInterval := flag.Int("ping-interval", 55, "心跳间隔(秒)，0 禁用")
	pingLimit := flag.Int("ping-limit", 0, "心跳无响应次数上限")
	routerMode := flag.String("router", "least_connections", "路由模式: random 或 least_connections")
	flag.Parse()

	// ============================
	// 关键步骤：注册自定义协议
	// ============================
	// 在创建 Gateway 之前注册。
	// 注册后，-listen 参数中使用 "jsonNL://ip:port" 即可启用该协议。
	gateway.RegisterProtocol("jsonNL", &JsonNLProtocol{})

	log.Println("[gateway-edit] 已注册自定义协议: jsonNL")
	log.Printf("[gateway-edit] 可用的内置协议: websocket, ws, tcp, frame, text")
	log.Printf("[gateway-edit] 已注册自定义协议: jsonNL")

	g := gateway.New(&gateway.Config{
		ListenAddrs:          strings.Split(*listen, ","),
		LanIP:                *lanIP,
		StartPort:            *startPort,
		InstanceID:           *instanceID,
		RegisterAddr:         strings.Split(*registerAddr, ","),
		SecretKey:            *secretKey,
		PingInterval:         *pingInterval,
		PingNotResponseLimit: *pingLimit,
		RouterMode:           gateway.RouterMode(*routerMode),
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[gateway-edit] Shutting down...")
		g.Stop()
		os.Exit(0)
	}()

	if err := g.Run(); err != nil {
		log.Fatalf("[gateway-edit] Fatal: %v", err)
	}
}
