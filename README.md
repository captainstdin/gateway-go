# GatewayWorker-Go

高性能、可扩展的 Go 实时通讯框架，基于 **Gateway + Worker 分离架构**。  
对标 PHP [GatewayWorker](https://github.com/walkor/GatewayWorker)，支持 WebSocket / WSS / TCP 多协议，AES 加密内部通讯，开箱即用。

---

## 架构

```
                         ┌──────────────────┐
                         │     Register     │  服务发现 & 地址广播
                         └─┬──────┬──────┬──┘
              注册/发现     │      │      │     发现 Gateway 地址
         ┌─────────────────┘      │      └────────────────────┐
         ▼                        │                           ▼
┌─────────────────┐               │              ┌─────────────────────┐
│    Gateway      │               │              │    GatewaySDK       │
│  管理客户端连接  │               │              │  外部进程主动推送    │
└──┬──────────┬───┘               │              └──────────┬──────────┘
   │          │ 内部 AES 加密通讯  │                        │
   │ WS/TCP   └───────────────────▼─────────────────────────┘
   ▼                   ┌────────────────────┐
┌──────┐               │   BusinessWorker   │  业务逻辑 / 事件回调
│Client│               └────────────────────┘
└──────┘
```

**数据流：**
- **被动事件**：Client → Gateway → Worker 触发 `OnConnect` / `OnMessage` / `OnClose`
- **Worker 内推送**：回调中调用 `gateway_api.SendToXxx()` → Gateway → Client
- **外部推送**：`GatewaySDK` 连接 Register 发现 Gateway → 发送指令 → Client

---

## 特性

- 🔌 **Gateway / Worker 分离** — 重启 Worker 不影响客户端连接（热更新业务逻辑）
- 🌐 **多协议支持** — `ws://` / `wss://` / `tcp://` / `frame://` / `text://` 及自定义协议
- 🔒 **AES-256-CBC 加密** — 所有内部通讯全程加密，SecretKey 双重认证
- ⚖️ **自动负载均衡** — 最少连接数 / 随机两种路由模式
- 📡 **GatewaySDK** — 从任意 Go 进程（HTTP 服务、定时任务、CLI）主动推送消息
- 🔄 **自动断线重连** — Gateway / Worker / Register 三方均支持
- 💓 **心跳检测** — 可配置间隔和超时踢出
- 🛡️ **多 Register 高可用** — 任一 Register 宕机不影响正常运行
- 📊 **Dashboard 监控面板** — 实时查看在线连接数 / Worker 处理次数
- 🔗 **完整 API** — 37 个方法对标 PHP 版全覆盖（UID 绑定、Group、Session、踢人等）

---

## 安装

> 仓库托管在公网 Gitea（`https://adminv.myds.me:3001`），无需额外鉴权，但由于使用了非标准端口，需配置 Go 跳过公共模块代理。

```bash
# 1. 告诉 Go 对该域名不走公共代理/校验
go env -w GOPRIVATE="adminv.myds.me:3001"
go env -w GONOSUMDB="adminv.myds.me:3001"
go env -w GONOSUMCHECK="adminv.myds.me:3001"

# 2. 安装
go get adminv.myds.me:3001/a/gatewayworker-go@latest
```

> **可选**：若遇到 TLS 证书校验错误（如使用自签证书），执行：
> ```bash
> go env -w GOINSECURE="adminv.myds.me:3001"
> ```

> 若使用 SSH 克隆，可在 `~/.gitconfig` 中添加 insteadOf 规则：
> ```
> [url "ssh://git@adminv.myds.me:222/"]
>     insteadOf = https://adminv.myds.me:3001/
> ```

---

## 快速开始

### 最小示例（Worker + echo 回显）

```go
package main

import (
    "fmt"
    "log"

    "adminv.myds.me/a/gatewayworker-go/pkg/gateway_api"
    "adminv.myds.me/a/gatewayworker-go/pkg/worker"
)

func main() {
    bw := worker.New(
        "my-worker",                 // Worker 名称
        0,                           // Worker ID（多实例时需不同）
        []string{"127.0.0.1:51234"}, // Register 地址
        "my-secret-key",             // 认证密钥
    )

    bw.OnWebSocketConnect = func(clientID string, data []byte) {
        gateway_api.SendToClient(clientID, []byte("Welcome!"))
    }

    bw.OnMessage = func(clientID string, message []byte) {
        gateway_api.SendToClient(clientID, []byte(fmt.Sprintf("echo: %s", message)))
    }

    gateway_api.SetBusinessWorker(bw)

    if err := bw.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### 三合一（单进程嵌入 Register + Gateway + Worker）

```go
package main

import (
    "log"

    "adminv.myds.me/a/gatewayworker-go/pkg/gateway"
    "adminv.myds.me/a/gatewayworker-go/pkg/gateway_api"
    "adminv.myds.me/a/gatewayworker-go/pkg/register"
    "adminv.myds.me/a/gatewayworker-go/pkg/worker"
)

func main() {
    const secretKey = "my-secret-key"

    // 1. Register
    r := register.New("0.0.0.0:51234", secretKey)
    go r.Run()

    // 2. Gateway
    g := gateway.New(&gateway.Config{
        ListenAddrs:  []string{"ws://0.0.0.0:7272"},
        RegisterAddr: []string{"127.0.0.1:51234"},
        SecretKey:    secretKey,
    })
    go g.Run()

    // 3. Worker（阻塞）
    bw := worker.New("worker", 0, []string{"127.0.0.1:51234"}, secretKey)
    bw.OnMessage = func(clientID string, msg []byte) {
        gateway_api.SendToClient(clientID, msg) // echo
    }
    gateway_api.SetBusinessWorker(bw)

    if err := bw.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### 外部推送（GatewaySDK）

```go
package main

import (
    "fmt"
    "adminv.myds.me/a/gatewayworker-go/pkg/gateway_sdk"
)

func main() {
    client := gateway_sdk.New(
        []string{"127.0.0.1:51234"}, // Register 地址
        "my-secret-key",
    )
    defer client.Close()

    // 广播给所有在线用户
    client.SendToAll([]byte(`{"type":"announcement","msg":"系统维护通知"}`))

    // 向指定 UID 推送
    client.SendToUID("user_100", []byte(`{"type":"notify","msg":"你有新消息"}`))

    // 查询在线人数
    count, _ := client.GetAllClientCount()
    fmt.Println("在线:", count)
}
```

---

## 以独立进程运行

```bash
# 编译
go build -o bin/register  ./cmd/register
go build -o bin/gateway   ./cmd/gateway
go build -o bin/worker    ./cmd/worker
go build -o bin/dashboard ./cmd/dashboard

# 启动（三个终端）
./bin/register -listen "text://0.0.0.0:51234" -key "my-secret-key"
./bin/gateway  -listen "ws://0.0.0.0:7272"    -key "my-secret-key" -register "127.0.0.1:51234"
./bin/worker                                   -key "my-secret-key" -register "127.0.0.1:51234"

# 可选：监控面板
./bin/dashboard -listen "0.0.0.0:8686" -key "my-secret-key" -register "127.0.0.1:51234"
```

WSS（WebSocket + TLS）：
```bash
./bin/gateway -listen "wss://0.0.0.0:7443" \
              -tls-cert cert.pem -tls-key key.pem \
              -key "my-secret-key"
```

---

## 包结构

```
pkg/
├── register/      注册中心（服务发现）
├── gateway/       网关（管理客户端连接）
├── worker/        业务 Worker（事件回调）
├── gateway_api/   Worker 内部推送 API（包级函数）
├── gateway_sdk/   外部进程推送 SDK（实例方法）
├── protocol/      内部二进制协议（GatewayProtocol）
├── crypto/        AES-256-CBC 加解密
└── context/       clientID 编解码工具

cmd/
├── register/      Register 独立可执行程序
├── gateway/       Gateway 独立可执行程序
├── worker/        Worker 独立可执行程序（内置 echo 示例）
├── dashboard/     监控面板
└── gateway-edit/  自定义协议示例
```

---

## 文档

| 文档 | 说明 |
|------|------|
| [架构原理](docs/architecture.md) | 系统设计、组件详解、通讯协议、安全机制 |
| [使用指南](docs/usage.md) | 快速开始、配置参数、部署方式、协议配置 |
| [代码开发指南](docs/code-usage.md) | Worker 事件回调、GatewaySDK 完整示例 |
| [GatewaySDK](docs/gateway-sdk.md) | 外部推送 SDK 完整 API 参考 |
| [速查手册](docs/cheatsheet.md) | 30 秒速览整个系统 |
| [开发注意事项](docs/best-practices.md) | 包大小限制、Session、并发安全 |
| [PHP vs Go 对比](docs/php-vs-go.md) | 与 PHP GatewayWorker 的完整对比 |

---

## License

MIT
