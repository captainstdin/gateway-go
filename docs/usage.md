# GatewayWorker-Go 使用指南

## 快速开始

### 编译

```bash
cd Gatewayworker-go
go build -o bin/register     ./cmd/register
go build -o bin/gateway      ./cmd/gateway
go build -o bin/worker       ./cmd/worker
go build -o bin/dashboard    ./cmd/dashboard
go build -o bin/gateway-edit ./cmd/gateway-edit  # 自定义协议示例
```

### 启动服务

```bash
# 终端1: Register
./bin/register -listen "text://0.0.0.0:51234" -key "my-secret-key"

# 终端2: Gateway（WebSocket + TCP 双协议）
./bin/gateway -listen "ws://0.0.0.0:7272,tcp://0.0.0.0:7273" -key "my-secret-key" -register "127.0.0.1:51234"

# 终端3: Worker（内置 echo 示例）
./bin/worker -key "my-secret-key" -register "127.0.0.1:51234"

# 终端4: Dashboard（可选）
./bin/dashboard -listen "0.0.0.0:8686" -key "my-secret-key" -register "127.0.0.1:51234"
```

### 测试连接

```javascript
// 浏览器控制台
const ws = new WebSocket('ws://127.0.0.1:7272');
ws.onopen = () => ws.send('Hello!');
ws.onmessage = (e) => console.log('收到:', e.data);
// 输出: 收到: echo: Hello!
```

### 监控面板

浏览器打开 `http://localhost:8686`，实时查看 Gateway 在线连接数和 Worker 处理次数。

---

## 监听地址格式

所有服务的 `-listen` 参数都支持 URI 风格的协议前缀：

| 协议前缀 | 说明 | 适用组件 |
|---------|------|---------|
| `text://` | 文本协议（JSON + AES + Base64） | Register |
| `tcp://` | 文本协议（同 text://） | Register |
| `ws://` | WebSocket 明文 | Gateway |
| `wss://` | WebSocket + TLS（需配置 `-tls-cert` / `-tls-key`） | Gateway |
| `tcp://` | TCP 4字节 body 长度头协议（Go 版默认） | Gateway |
| `frame://` | TCP 4字节总包长头协议（与 PHP Workerman frame 一致） | Gateway |
| `text://` | 换行符分隔文本协议（与 PHP Workerman text 一致） | Gateway |
| `自定义://` | 自定义 TCP 协议（需先注册） | Gateway |

不带前缀时，Register 默认 text，Gateway 默认 ws（明文 WebSocket）。

**Gateway 多协议监听**（逗号分隔）：

```bash
# 同时监听 WS 和 TCP
./gateway -listen "ws://0.0.0.0:7272,tcp://0.0.0.0:7273" -key "xxx"

# 只开 WS
./gateway -listen "ws://0.0.0.0:7272" -key "xxx"

# 开启 WSS（需要证书）
./gateway -listen "wss://0.0.0.0:7443" -tls-cert cert.pem -tls-key key.pem -key "xxx"

# WS + WSS 双协议同时监听
./gateway -listen "ws://0.0.0.0:7272,wss://0.0.0.0:7443" -tls-cert cert.pem -tls-key key.pem -key "xxx"

# 使用自定义协议（需在代码中先 RegisterProtocol）
./gateway -listen "ws://0.0.0.0:7272,jsonNL://0.0.0.0:7273" -key "xxx"
```

---

## 自定义 TCP 协议

Gateway 支持自定义 TCP 应用层协议，参考 PHP Workerman 的 `ProtocolInterface` 设计。

### 协议接口

实现 `ClientProtocol` 接口即可定义自己的协议，接口定义在 `pkg/gateway/client_protocol.go`：

```go
type ClientProtocol interface {
    // Input 从 TCP 流缓冲区中判断一个完整包的长度
    //  返回 > 0: 完整包的总长度，框架会凑齐后调用 Decode
    //  返回 == 0: 数据不够，继续等待
    //  返回 < 0: 协议错误，关闭连接
    Input(buf []byte) int

    // Decode 将完整的原始数据包解码为业务数据
    Decode(buf []byte) []byte

    // Encode 将业务数据编码为协议格式字节
    Encode(data []byte) []byte
}
```

### 工作流程

```
客户端发送数据 → TCP 流 → Input(buf) 分包
                            ↓ 凑齐完整包
                        Decode(packet) 解包
                            ↓
                     OnMessage(clientID, data) 业务回调

业务回调 send(data) → Encode(data) 打包 → 发送给客户端
```

### 示例：JsonNL 协议

以换行符 `\n` 作为包分隔符，数据格式为 JSON（与 PHP Workerman 的 JsonNL 协议一致）：

```go
package main

import (
    "bytes"
    "gatewayworker-go/pkg/gateway"
)

// JsonNLProtocol 以 \n 分隔的 JSON 文本协议
type JsonNLProtocol struct{}

func (p *JsonNLProtocol) Input(buf []byte) int {
    pos := bytes.IndexByte(buf, '\n')
    if pos < 0 {
        return 0 // 没有换行符，继续等待
    }
    return pos + 1 // 包含换行符的完整包长度
}

func (p *JsonNLProtocol) Decode(buf []byte) []byte {
    return bytes.TrimRight(buf, "\n")
}

func (p *JsonNLProtocol) Encode(data []byte) []byte {
    return append(data, '\n')
}
```

### 注册和使用

完整的自定义协议示例参见 `cmd/gateway-edit/main.go`。

**编译**：

```bash
go build -o bin/gateway-edit ./cmd/gateway-edit
```

**启动**（使用自定义 JsonNL 协议）：

```bash
# 终端1: Register
./bin/register -listen "text://0.0.0.0:51234" -key "my-secret-key"

# 终端2: Gateway（使用自定义 jsonNL 协议）
./bin/gateway-edit -listen "jsonNL://0.0.0.0:7273" -key "my-secret-key" -register "127.0.0.1:51234"

# 或者 WebSocket + 自定义协议双监听
./bin/gateway-edit -listen "ws://0.0.0.0:7272,jsonNL://0.0.0.0:7273" -key "my-secret-key" -register "127.0.0.1:51234"

# 或者使用内置 text 协议（无需自定义，标准 gateway 也支持）
./bin/gateway -listen "text://0.0.0.0:7273" -key "my-secret-key" -register "127.0.0.1:51234"

# 终端3: Worker
./bin/worker -key "my-secret-key" -register "127.0.0.1:51234"
```

**客户端测试**（telnet）：

```bash
telnet 127.0.0.1 7273
{"type":"chat","msg":"hello"}
# 回车发送，Worker 会收到 OnMessage 回调
```

**关键代码**（cmd/gateway-edit/main.go 中的核心步骤）：

```go
// 1. 实现 ClientProtocol 接口
type JsonNLProtocol struct{}
func (p *JsonNLProtocol) Input(buf []byte) int  { /* 找 \n */ }
func (p *JsonNLProtocol) Decode(buf []byte) []byte { /* 去掉 \n */ }
func (p *JsonNLProtocol) Encode(data []byte) []byte { /* 加 \n */ }

// 2. 在 main() 中注册
gateway.RegisterProtocol("jsonNL", &JsonNLProtocol{})

// 3. 通过 -listen "jsonNL://0.0.0.0:7273" 启动
```

### 内置协议

| 协议前缀 | 包格式 | 说明 |
|--------|--------|------|
| `ws://` | WebSocket 帧 | WebSocket 明文连接 |
| `wss://` | WebSocket 帧 + TLS | WebSocket 加密连接，需配置 `-tls-cert` / `-tls-key` |
| `tcp://` | `[4B body长度][body]` | 4字节大端 **body 长度** + body，Go 版默认 TCP 协议 |
| `frame://` | `[4B 总包长][body]` | 4字节大端 **总包长**(含头) + body，与 PHP Workerman frame 一致 |
| `text://` | `数据\n` | 换行符 `\n` 分隔的文本协议，适合 telnet 调试 |

### 与 PHP Workerman 对比

| PHP | Go | 说明 |
|-----|-----|------|
| `ProtocolInterface::input($buffer)` | `ClientProtocol.Input(buf) int` | 分包 |
| `ProtocolInterface::decode($buffer)` | `ClientProtocol.Decode(buf) []byte` | 解包 |
| `ProtocolInterface::encode($data)` | `ClientProtocol.Encode(data) []byte` | 打包 |
| `new Gateway("JsonNL://...")` 自动加载 | `RegisterProtocol("jsonNL", &P{})` 手动注册 | Go 是静态语言 |
| 协议类放在 `Protocols/` 目录自动发现 | 在 `main()` 或 `init()` 中显式注册 | 编译时确定 |

---

## 配置参数

### Register

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `text://0.0.0.0:51234` | 监听地址 |
| `-key` | `""` | 认证密钥（同时用于 AES 加密） |

### Gateway

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `ws://0.0.0.0:7272` | 监听地址，逗号分隔多个 |
| `-lan-ip` | `127.0.0.1` | 内网 IP（分布式部署时设为本机内网 IP）|
| `-start-port` | `54321` | 内部通讯起始端口 |
| `-id` | `0` | 实例 ID（多实例时需不同）|
| `-register` | `127.0.0.1:51234` | Register 地址，逗号分隔多个 |
| `-key` | `""` | 认证密钥 |
| `-tls-cert` | `""` | TLS 证书文件（PEM 格式），`wss://` 时必填 |
| `-tls-key` | `""` | TLS 私钥文件（PEM 格式），`wss://` 时必填 |
| `-ping-interval` | `55` | 心跳间隔（秒），0 禁用 |
| `-ping-limit` | `0` | 心跳未响应上限，0 不检测 |
| `-router` | `least_connections` | 路由模式：`random` / `least_connections` |

### Worker

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-name` | `worker` | Worker 名称 |
| `-id` | `0` | Worker ID |
| `-register` | `127.0.0.1:51234` | Register 地址，逗号分隔多个 |
| `-key` | `""` | 认证密钥 |

### Dashboard

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `0.0.0.0:8686` | Web 监听地址 |
| `-register` | `127.0.0.1:51234` | Register 地址，逗号分隔多个 |
| `-key` | `""` | 认证密钥 |

---

## 业务开发

自定义业务逻辑只需修改 `cmd/worker/main.go` 中的回调函数。

### 聊天室示例

```go
package main

import (
    "encoding/json"
    "gatewayworker-go/pkg/gateway_api"
    "gatewayworker-go/pkg/worker"
    "log"
)

func main() {
    bw := worker.New("chat", 0, []string{"127.0.0.1:51234"}, "my-key")

    bw.OnConnect = func(clientID string) {
        log.Printf("新连接: %s", clientID)
        gateway_api.SendToClient(clientID, []byte(`{"type":"welcome","msg":"欢迎"}`))
    }

    bw.OnMessage = func(clientID string, message []byte) {
        var msg map[string]string
        json.Unmarshal(message, &msg)

        switch msg["type"] {
        case "login":
            gateway_api.BindUID(clientID, msg["uid"])
            gateway_api.JoinGroup(clientID, "room_1")

        case "chat":
            gateway_api.SendToGroup("room_1", message)

        case "private":
            gateway_api.SendToUID(msg["to_uid"], message)
        }
    }

    bw.OnClose = func(clientID string) {
        log.Printf("断开: %s", clientID)
    }

    gateway_api.SetBusinessWorker(bw)
    bw.Run()
}
```

### 可用回调

| 回调 | 触发时机 |
|------|---------|
| `OnWorkerStart` | Worker 启动时 |
| `OnWorkerStop` | Worker 停止时 |
| `OnConnect` | 客户端连接 Gateway 时 |
| `OnMessage` | 客户端发送消息时 |
| `OnClose` | 客户端断开时 |
| `OnWebSocketConnect` | WebSocket 握手完成时（携带 HTTP 头信息）|

---

## GatewaySDK SDK

用于从 **Worker 外部的其他进程** 向客户端推送消息。完整 API 参考见 [GatewaySDK SDK 文档](gateway-sdk.md)。

### 典型场景

- HTTP API 收到请求后推送实时通知
- 管理后台踢出某个用户
- 定时任务向所有在线用户广播

### 使用方式

```go
package main

import (
    "fmt"
    "gatewayworker-go/pkg/gateway_sdk"
)

func main() {
    client := gateway_sdk.New(
        []string{"127.0.0.1:51234"},
        "my-secret-key",
    )
    defer client.Close()

    // 推送给指定 UID
    client.SendToUID("user_100", []byte(`{"type":"notify","msg":"你有新消息"}`))

    // 广播
    client.SendToAll([]byte(`{"type":"announcement","msg":"系统维护通知"}`))

    // 查询在线人数
    count, _ := client.GetAllClientCount()
    fmt.Println("在线人数:", count)

    // 判断是否在线
    online, _ := client.IsOnline("某个client_id")
    fmt.Println("在线:", online)

    // 踢出
    client.CloseClient("某个client_id", []byte("你被踢出了"))

    // UID/Group 操作
    client.BindUID("某个client_id", "user_200")
    client.JoinGroup("某个client_id", "room_5")
    client.SendToGroup("room_5", []byte(`{"msg":"hello room"}`))
}
```

### GatewaySDK vs gateway_api

| 特性 | `gateway_api` | `GatewaySDK` |
|------|---------------|-----------------|
| **使用位置** | Worker 回调内部 | 任意外部 Go 进程 |
| **连接方式** | 复用 Worker 已有连接 | 自建连接池 |
| **发现 Gateway** | Worker 内部地址表 | 连接 Register 查询 |
| **适用场景** | 事件响应 | HTTP推送、定时任务、管理操作 |

---

## API 参考

### 消息发送

| 方法 | 说明 |
|------|------|
| `SendToClient(clientID, message)` | 向指定客户端发送 |
| `SendToAll(message)` | 广播给所有在线客户端 |
| `SendToUID(uid, message)` | 向指定 UID 发送 |
| `SendToGroup(group, message)` | 向指定分组广播 |

### UID 操作

| 方法 | 说明 |
|------|------|
| `BindUID(clientID, uid)` | 绑定（一个 uid 可绑多个 client_id） |
| `UnbindUID(clientID, uid)` | 解绑 |

### Group 操作

| 方法 | 说明 |
|------|------|
| `JoinGroup(clientID, group)` | 加入分组 |
| `LeaveGroup(clientID, group)` | 离开分组 |
| `Ungroup(group)` | 解散分组 |

### Session 操作

| 方法 | 说明 |
|------|------|
| `SetSession(clientID, session)` | 覆盖 session |
| `UpdateSession(clientID, session)` | 合并 session |

### 连接管理

| 方法 | 说明 |
|------|------|
| `CloseClient(clientID, message)` | 踢出（先发消息再断开） |
| `DestroyClient(clientID)` | 直接销毁 |

### 查询类（GatewaySDK 和 gateway_api 均可用）

| 方法 | 说明 |
|------|------|
| `IsOnline(clientID)` | 判断 client_id 是否在线 |
| `IsUidOnline(uid)` | 判断 uid 是否在线 |
| `GetAllClientCount()` | 获取在线连接总数 |
| `GetClientCountByGroup(group)` | 获取分组在线连接数 |
| `GetSession(clientID)` | 获取 session |
| `GetAllClientSessions()` | 所有客户端 session |
| `GetClientSessionsByGroup(group)` | 分组成员 session |
| `GetAllClientIdList()` | 所有在线 client_id 列表 |
| `GetClientIdListByGroup(group)` | 分组成员 client_id 列表 |
| `GetClientIdByUid(uid)` | 获取 uid 绑定的 client_id |
| `GetUidByClientId(clientID)` | 通过 client_id 获取 uid |
| `GetAllUidList()` | 全局在线 uid 列表 |
| `GetAllUidCount()` | 全局在线 uid 数量 |
| `GetUidListByGroup(group)` | 分组在线 uid 列表 |
| `GetAllGroupIdList()` | 所有在线分组 ID 列表 |

> 完整 API 和用法示例见 [GatewaySDK SDK 文档](gateway-sdk.md)


---

## 分布式部署

### 单机部署

```bash
./register  -listen "text://0.0.0.0:51234" -key "xxx"
./gateway   -listen "ws://0.0.0.0:7272" -key "xxx"
./worker    -key "xxx"
./dashboard -listen "0.0.0.0:8686" -key "xxx"
```

### 多机部署

```bash
# 服务器A: Register
./register -listen "text://0.0.0.0:51234" -key "xxx"

# 服务器B: Gateway
./gateway -listen "ws://0.0.0.0:7272" -lan-ip "192.168.1.2" -key "xxx" \
    -register "192.168.1.1:51234"

# 服务器C: Worker
./worker -key "xxx" -register "192.168.1.1:51234"
```

> **注意**：跨机部署时 Gateway 必须设置 `-lan-ip` 为本机内网 IP。

### 多 Register 高可用

```bash
# 两台 Register
./register -listen "text://0.0.0.0:51234" -key "xxx"   # 服务器A
./register -listen "text://0.0.0.0:51234" -key "xxx"   # 服务器B

# Gateway/Worker 填多个 Register（逗号分隔）
./gateway -listen "ws://0.0.0.0:7272" -key "xxx" \
    -register "192.168.1.10:51234,192.168.1.11:51234"

./worker -key "xxx" -register "192.168.1.10:51234,192.168.1.11:51234"
```

### 多 Gateway 实例

```bash
./gateway -id 0 -listen "ws://0.0.0.0:7272" -key "xxx"   # 内部端口 54321
./gateway -id 1 -listen "ws://0.0.0.0:7273" -key "xxx"   # 内部端口 54322
```

### 多 Worker 实例

```bash
./worker -name "worker" -id 0 -key "xxx"
./worker -name "worker" -id 1 -key "xxx"
```

---

## WSS / TLS 部署

Gateway 原生支持 WSS，无需前置反代。

### 生成自签证书（测试用）

```bash
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout key.pem -out cert.pem \
  -days 365 -subj "/CN=localhost"
```

### 启动带 TLS 的 Gateway

```bash
# 纯 WSS
./gateway -listen "wss://0.0.0.0:7443" \
          -tls-cert cert.pem -tls-key key.pem \
          -key "my-secret-key"

# WS + WSS 双端口（兼容旧客户端）
./gateway -listen "ws://0.0.0.0:7272,wss://0.0.0.0:7443" \
          -tls-cert cert.pem -tls-key key.pem \
          -key "my-secret-key"
```

### 客户端连接

```javascript
// 浏览器
const ws = new WebSocket('wss://yourdomain.com:7443');
ws.onopen = () => ws.send('Hello WSS!');
```

### 注意事项

> [!NOTE]
> 使用自签证书时浏览器会拒绝 WSS 连接。生产环境请使用 Let's Encrypt 或商业证书。
> Caddy 可自动申请并续期 Let's Encrypt 证书，是最简单的生产方案：
>
> ```caddy
> wss.yourdomain.com {
>     reverse_proxy localhost:7272   # 反代到 ws:// Gateway
> }
> ```
>
> 也可以继续使用 Nginx 作为 TLS 终止层，Gateway 本身监听 `ws://`，对外由 Nginx 暴露 `wss://`。两种方式均支持，按运维习惯选择。
