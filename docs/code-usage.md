# GatewayWorker-Go 代码开发指南

本文档面向 **使用 GatewayWorker-Go 编写业务逻辑** 的开发者，涵盖两大核心用法：

1. **Worker 被动事件回调** — 在 Worker 进程中响应客户端连接/消息/断开等事件
2. **GatewayClient 主动调用** — 从任意外部 Go 进程（HTTP 服务、定时任务等）向客户端推送消息

---

## 目录

- [架构概览](#架构概览)
- [Worker 被动事件开发](#worker-被动事件开发)
  - [最小示例](#最小示例)
  - [事件回调一览](#事件回调一览)
  - [OnWorkerStart / OnWorkerStop](#onworkerstart--onworkerstop)
  - [OnConnect](#onconnect)
  - [OnWebSocketConnect](#onwebsocketconnect)
  - [OnMessage](#onmessage)
  - [OnClose](#onclose)
  - [gateway_api 函数列表](#gateway_api-函数列表)
  - [完整示例：聊天室](#完整示例聊天室)
- [GatewayClient 主动调用](#gatewayclient-主动调用)
  - [创建客户端](#创建客户端)
  - [API 一览](#api-一览)
  - [完整示例：HTTP 推送服务](#完整示例http-推送服务)
  - [完整示例：管理后台踢人](#完整示例管理后台踢人)
- [gateway_api vs GatewayClient 对比](#gateway_api-vs-gatewayclient-对比)
- [常见模式](#常见模式)

---

## 架构概览

```
                          ┌──────────────────────────────────┐
                          │          Register                │
                          │    服务发现 & 地址广播              │
                          └──────┬───────────────┬───────────┘
                                 │               │
                    ┌────────────▼──┐        ┌───▼────────────┐
                    │   Gateway     │◄──────►│    Worker      │
                    │  持有客户端连接 │        │  业务逻辑处理    │
                    └───────▲──────┘        └────────────────┘
                            │                    ▲
                    ┌───────┴──────┐              │
                    │   客户端      │         ┌────┴───────────┐
                    │  (浏览器/App) │         │ GatewayClient  │
                    └──────────────┘         │ (HTTP/定时任务)  │
                                             └────────────────┘
```

**数据流向：**

- **被动事件**：客户端 → Gateway → Worker（触发 `OnConnect`/`OnMessage`/`OnClose` 回调）
- **Worker 内主动推送**：Worker 回调中调用 `gateway_api.SendToXxx()` → Gateway → 客户端
- **外部主动推送**：外部进程创建 `GatewayClient` → 连接 Register 发现 Gateway → 发送指令 → Gateway → 客户端

---

## Worker 被动事件开发

Worker 是业务逻辑的核心。你只需要创建一个 `BusinessWorker`，注册事件回调函数，然后在回调中通过 `gateway_api` 包与客户端交互。

### 最小示例

```go
package main

import (
    "fmt"
    "gatewayworker-go/pkg/gateway_api"
    "gatewayworker-go/pkg/worker"
    "log"
)

func main() {
    // 1. 创建 Worker
    bw := worker.New(
        "my-worker",                    // Worker 名称
        0,                              // Worker ID（多实例时需不同）
        []string{"127.0.0.1:51234"},    // Register 地址
        "my-secret-key",                // 认证密钥
    )

    // 2. 注册事件回调
    bw.OnMessage = func(clientID string, message []byte) {
        // 收到消息后回显
        gateway_api.SendToClient(clientID, []byte(fmt.Sprintf("echo: %s", message)))
    }

    // 3. 绑定 Worker 到 gateway_api（必须）
    gateway_api.SetBusinessWorker(bw)

    // 4. 启动（阻塞运行）
    if err := bw.Run(); err != nil {
        log.Fatal(err)
    }
}
```

> **关键步骤**：调用 `gateway_api.SetBusinessWorker(bw)` 是必须的，否则 `gateway_api` 包中的所有函数都无法工作。

---

### 事件回调一览

| 回调 | 签名 | 触发时机 |
|------|------|---------|
| `OnWorkerStart` | `func()` | Worker 进程启动完成 |
| `OnWorkerStop` | `func()` | Worker 进程停止时 |
| `OnConnect` | `func(clientID string)` | 客户端 TCP 连接建立（尚未完成协议握手） |
| `OnWebSocketConnect` | `func(clientID string, data []byte)` | WebSocket 握手完成（`data` 包含 HTTP 头信息） |
| `OnMessage` | `func(clientID string, message []byte)` | 客户端发送消息 |
| `OnClose` | `func(clientID string)` | 客户端断开连接 |

**触发顺序**（以 WebSocket 为例）：

```
OnConnect → OnWebSocketConnect → OnMessage (N次) → OnClose
```

**触发顺序**（以 TCP 为例）：

```
OnConnect → OnMessage (N次) → OnClose
```

---

### OnWorkerStart / OnWorkerStop

用于 Worker 生命周期管理，适合做初始化和清理工作。

```go
bw.OnWorkerStart = func() {
    log.Println("Worker 启动，初始化数据库连接...")
    // 初始化数据库、缓存等资源
}

bw.OnWorkerStop = func() {
    log.Println("Worker 停止，清理资源...")
    // 关闭数据库连接、刷新缓存等
}
```

---

### OnConnect

客户端 TCP 连接建立时触发。此时客户端尚未发送任何数据。

```go
bw.OnConnect = func(clientID string) {
    log.Printf("新连接: %s", clientID)
    // 注意：WebSocket 客户端在这里还没完成握手，
    // 建议在 OnWebSocketConnect 中做欢迎消息等操作
}
```

> **clientID** 是全局唯一的 20 字节十六进制字符串（如 `7f000001d43100000001`），编码了 Gateway 的 IP、端口和连接序号。

---

### OnWebSocketConnect

WebSocket 协议握手完成后触发，`data` 包含 HTTP 请求头信息（JSON 格式）。

```go
bw.OnWebSocketConnect = func(clientID string, data []byte) {
    log.Printf("WebSocket 就绪: %s", clientID)

    // data 包含 HTTP 头，可用于鉴权
    // 例如解析 Cookie、Token 等

    // 发送欢迎消息
    gateway_api.SendToClient(clientID, []byte("Welcome! Your ID: "+clientID))
}
```

---

### OnMessage

客户端发送消息时触发，这是最核心的业务回调。

```go
bw.OnMessage = func(clientID string, message []byte) {
    // message 就是客户端发送的原始数据（经过协议层 Decode 之后的）

    // 示例：解析 JSON 消息并分发处理
    var msg struct {
        Type string `json:"type"`
        Data string `json:"data"`
    }
    if err := json.Unmarshal(message, &msg); err != nil {
        gateway_api.SendToClient(clientID, []byte(`{"error":"invalid json"}`))
        return
    }

    switch msg.Type {
    case "chat":
        // 广播给所有人
        gateway_api.SendToAll(message, nil, nil)
    case "ping":
        // 回复 pong
        gateway_api.SendToClient(clientID, []byte(`{"type":"pong"}`))
    }
}
```

---

### OnClose

客户端断开连接时触发，适合做清理工作。

```go
bw.OnClose = func(clientID string) {
    log.Printf("客户端断开: %s", clientID)
    // 从业务层面清理该用户的状态
    // 注意：UID 绑定和 Group 关系会被 Gateway 自动清理，无需手动处理
}
```

---

### gateway_api 函数列表

在 Worker 回调函数内部，通过 `gateway_api` 包进行消息推送和连接管理：

```go
import "gatewayworker-go/pkg/gateway_api"
```

#### 消息发送

```go
// 向指定客户端发送消息
gateway_api.SendToClient(clientID string, message []byte)

// 广播给所有在线客户端
// clientIDs: 限定目标列表（nil 表示所有人）
// excludeIDs: 排除列表（nil 表示不排除）
gateway_api.SendToAll(message []byte, clientIDs []string, excludeIDs []string)

// 向指定 UID 发送（支持单个 string 或 []interface{}）
gateway_api.SendToUID(uid interface{}, message []byte)

// 向指定分组广播（支持单个 string 或 []interface{}）
gateway_api.SendToGroup(group interface{}, message []byte)
```

#### UID 绑定

```go
// 绑定 UID（一个 UID 可以绑定多个 clientID，实现多端登录）
gateway_api.BindUID(clientID string, uid string)

// 解除绑定
gateway_api.UnbindUID(clientID string, uid string)
```

#### 分组操作

```go
// 加入分组
gateway_api.JoinGroup(clientID string, group string)

// 离开分组
gateway_api.LeaveGroup(clientID string, group string)

// 解散整个分组（移除所有成员）
gateway_api.Ungroup(group string)
```

#### Session 操作

```go
// 覆盖 session（整个替换）
gateway_api.SetSession(clientID string, session map[string]interface{})

// 合并 session（只更新传入的 key）
gateway_api.UpdateSession(clientID string, session map[string]interface{})
```

#### 连接管理

```go
// 踢出客户端（先发送 message 再断开）
gateway_api.CloseClient(clientID string, message []byte)

// 直接销毁连接（不发送任何消息）
gateway_api.DestroyClient(clientID string)
```

---

### 完整示例：聊天室

```go
package main

import (
    "encoding/json"
    "fmt"
    "gatewayworker-go/pkg/gateway_api"
    "gatewayworker-go/pkg/worker"
    "log"
    "os"
    "os/signal"
    "syscall"
)

// ChatMsg 聊天消息格式
type ChatMsg struct {
    Type   string `json:"type"`              // login | chat | private
    Name   string `json:"name,omitempty"`     // 用户名（login 时）
    To     string `json:"to,omitempty"`       // 目标 UID（private 时）
    Msg    string `json:"msg,omitempty"`      // 消息内容
    From   string `json:"from,omitempty"`     // 发送者（服务端填充）
}

func main() {
    bw := worker.New("chat", 0, []string{"127.0.0.1:51234"}, "my-key")

    bw.OnWorkerStart = func() {
        log.Println("聊天室 Worker 已启动")
    }

    bw.OnWebSocketConnect = func(clientID string, data []byte) {
        welcome, _ := json.Marshal(ChatMsg{
            Type: "system",
            Msg:  fmt.Sprintf("连接成功，你的 ID 是 %s，请发送 login 消息设置昵称", clientID),
        })
        gateway_api.SendToClient(clientID, welcome)
    }

    bw.OnMessage = func(clientID string, message []byte) {
        var msg ChatMsg
        if err := json.Unmarshal(message, &msg); err != nil {
            errResp, _ := json.Marshal(ChatMsg{Type: "error", Msg: "无效的 JSON 格式"})
            gateway_api.SendToClient(clientID, errResp)
            return
        }

        switch msg.Type {
        case "login":
            // 绑定 UID 并加入默认聊天室
            gateway_api.BindUID(clientID, msg.Name)
            gateway_api.JoinGroup(clientID, "chatroom")

            // 设置 session 存储用户信息
            gateway_api.SetSession(clientID, map[string]interface{}{
                "name": msg.Name,
            })

            // 通知全房间
            notice, _ := json.Marshal(ChatMsg{
                Type: "system",
                Msg:  fmt.Sprintf("'%s' 加入了聊天室", msg.Name),
            })
            gateway_api.SendToGroup("chatroom", notice)

        case "chat":
            // 群聊：广播给房间所有人
            broadcast, _ := json.Marshal(ChatMsg{
                Type: "chat",
                From: msg.Name,
                Msg:  msg.Msg,
            })
            gateway_api.SendToGroup("chatroom", broadcast)

        case "private":
            // 私聊：发给指定 UID
            pm, _ := json.Marshal(ChatMsg{
                Type: "private",
                From: msg.Name,
                Msg:  msg.Msg,
            })
            gateway_api.SendToUID(msg.To, pm)
        }
    }

    bw.OnClose = func(clientID string) {
        log.Printf("用户断开: %s", clientID)
    }

    gateway_api.SetBusinessWorker(bw)

    // 优雅退出
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigCh
        bw.Stop()
        os.Exit(0)
    }()

    if err := bw.Run(); err != nil {
        log.Fatal(err)
    }
}
```

**客户端交互流程（JavaScript）：**

```javascript
const ws = new WebSocket('ws://127.0.0.1:7272');

ws.onopen = () => {
    // 登录
    ws.send(JSON.stringify({ type: 'login', name: 'Alice' }));
};

ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    console.log(`[${msg.type}] ${msg.from || 'system'}: ${msg.msg}`);
};

// 发送群聊消息
ws.send(JSON.stringify({ type: 'chat', name: 'Alice', msg: '大家好！' }));

// 发送私聊消息
ws.send(JSON.stringify({ type: 'private', name: 'Alice', to: 'Bob', msg: '你好 Bob' }));
```

---

## GatewayClient 主动调用

`GatewayClient` 用于从 **Worker 外部的任意 Go 进程** 主动向客户端推送消息，无需运行在 Worker 进程内。

### 创建客户端

```go
import "gatewayworker-go/pkg/gateway_client"

// 创建 GatewayClient（自动连接 Register 发现 Gateway 地址）
client := gateway_client.New(
    []string{"127.0.0.1:51234"},  // Register 地址（支持多个）
    "my-secret-key",              // 认证密钥（必须与 Gateway/Register 一致）
)
defer client.Close()  // 用完记得关闭
```

**内部机制：**
1. 连接 Register 获取所有 Gateway 内部地址
2. 维护到每个 Gateway 的 TCP 连接池（自动重连，50 秒自动刷新）
3. 所有通信通过 AES 加密

---

### API 一览

#### 消息发送

```go
// 向指定客户端发送
err := client.SendToClient(clientID string, message []byte)

// 广播给所有在线客户端
err := client.SendToAll(message []byte)

// 向指定 UID 发送
err := client.SendToUID(uid interface{}, message []byte)

// 向指定分组广播
err := client.SendToGroup(group interface{}, message []byte)
```

#### UID / Group 操作

```go
err := client.BindUID(clientID, uid string)
err := client.UnbindUID(clientID, uid string)
err := client.JoinGroup(clientID, group string)
err := client.LeaveGroup(clientID, group string)
```

#### Session 操作

```go
err := client.SetSession(clientID string, session map[string]interface{})
err := client.UpdateSession(clientID string, session map[string]interface{})
```

#### 连接管理

```go
// 踢出（先发 message 再断开）
err := client.CloseClient(clientID string, message []byte)

// 直接销毁
err := client.DestroyClient(clientID string)
```

#### 查询（仅 GatewayClient 可用）

```go
// 判断客户端是否在线
online, err := client.IsOnline(clientID string)

// 获取所有在线连接总数
count, err := client.GetAllClientCount()
```

> **注意**：`IsOnline` 和 `GetAllClientCount` 是 GatewayClient 独有的，`gateway_api` 包中没有这些方法。因为它们需要从 Gateway 查询并等待响应，而 Worker 内部的 `gateway_api` 是单向发送。

---

### 完整示例：HTTP 推送服务

在 HTTP API 中接收请求，通过 GatewayClient 向客户端推送实时通知：

```go
package main

import (
    "encoding/json"
    "fmt"
    "gatewayworker-go/pkg/gateway_client"
    "log"
    "net/http"
)

var gwClient *gateway_client.GatewayClient

func main() {
    // 创建 GatewayClient（全局复用）
    gwClient = gateway_client.New(
        []string{"127.0.0.1:51234"},
        "my-secret-key",
    )
    defer gwClient.Close()

    http.HandleFunc("/api/notify", handleNotify)
    http.HandleFunc("/api/broadcast", handleBroadcast)
    http.HandleFunc("/api/online-count", handleOnlineCount)
    http.HandleFunc("/api/kick", handleKick)

    log.Println("HTTP 推送服务启动在 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

// POST /api/notify   { "uid": "user_100", "msg": "你有新订单" }
func handleNotify(w http.ResponseWriter, r *http.Request) {
    var req struct {
        UID string `json:"uid"`
        Msg string `json:"msg"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    payload, _ := json.Marshal(map[string]string{
        "type": "notification",
        "msg":  req.Msg,
    })

    if err := gwClient.SendToUID(req.UID, payload); err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    fmt.Fprintln(w, `{"status":"ok"}`)
}

// POST /api/broadcast   { "msg": "系统将于 22:00 维护" }
func handleBroadcast(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Msg string `json:"msg"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    payload, _ := json.Marshal(map[string]string{
        "type": "announcement",
        "msg":  req.Msg,
    })

    if err := gwClient.SendToAll(payload); err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    fmt.Fprintln(w, `{"status":"ok"}`)
}

// GET /api/online-count
func handleOnlineCount(w http.ResponseWriter, r *http.Request) {
    count, err := gwClient.GetAllClientCount()
    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    fmt.Fprintf(w, `{"count":%d}`, count)
}

// POST /api/kick   { "client_id": "7f000001d43100000001", "reason": "违规操作" }
func handleKick(w http.ResponseWriter, r *http.Request) {
    var req struct {
        ClientID string `json:"client_id"`
        Reason   string `json:"reason"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    msg, _ := json.Marshal(map[string]string{
        "type": "kicked",
        "msg":  req.Reason,
    })

    if err := gwClient.CloseClient(req.ClientID, msg); err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    fmt.Fprintln(w, `{"status":"ok"}`)
}
```

**测试：**

```bash
# 推送给指定用户
curl -X POST http://localhost:8080/api/notify \
  -d '{"uid":"user_100","msg":"你有新订单"}'

# 全服广播
curl -X POST http://localhost:8080/api/broadcast \
  -d '{"msg":"系统将于 22:00 维护"}'

# 查询在线人数
curl http://localhost:8080/api/online-count

# 踢出指定客户端
curl -X POST http://localhost:8080/api/kick \
  -d '{"client_id":"7f000001d43100000001","reason":"违规操作"}'
```

---

### 完整示例：管理后台踢人

```go
package main

import (
    "fmt"
    "gatewayworker-go/pkg/gateway_client"
    "log"
)

func main() {
    client := gateway_client.New(
        []string{"127.0.0.1:51234"},
        "my-secret-key",
    )
    defer client.Close()

    // 查看在线人数
    count, err := client.GetAllClientCount()
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("当前在线: %d 人\n", count)

    // 检查某用户是否在线
    targetID := "7f000001d43100000001"
    online, err := client.IsOnline(targetID)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("用户 %s 在线: %v\n", targetID, online)

    // 踢出并发送提示
    if online {
        client.CloseClient(targetID, []byte(`{"type":"kicked","msg":"您的账号在其他设备登录"}`))
        fmt.Println("已踢出")
    }
}
```

---

## gateway_api vs GatewayClient 对比

| 维度 | `gateway_api` | `GatewayClient` |
|------|---------------|-----------------|
| **所在进程** | Worker 进程内部 | 任意外部 Go 进程 |
| **使用位置** | 事件回调函数中 | HTTP Handler、定时任务、CLI 工具等 |
| **连接方式** | 复用 Worker 已有的 Gateway 连接 | 自建连接池（连 Register → 发现 Gateway） |
| **初始化** | `gateway_api.SetBusinessWorker(bw)` | `gateway_client.New(addrs, key)` |
| **函数风格** | 包级函数，无 error 返回 | 实例方法，返回 `error` |
| **查询能力** | ❌ 无 | ✅ `IsOnline`、`GetAllClientCount` |
| **适用场景** | 响应客户端事件 | 主动推送、管理操作 |

**选择原则：**

- 在 Worker 回调里处理事件 → 用 `gateway_api`
- 在其他进程里主动推送 → 用 `GatewayClient`

---

## 常见模式

### 1. 多端登录

一个 UID 绑定多个 clientID，发送到 UID 时所有终端都能收到：

```go
// Worker 中
bw.OnWebSocketConnect = func(clientID string, data []byte) {
    // 假设从 HTTP 头中解析出了 token，查到了 userID
    userID := parseUserID(data)
    gateway_api.BindUID(clientID, userID)
}

// 外部推送时
client.SendToUID("user_100", payload) // user_100 的所有终端都会收到
```

### 2. 房间/频道

用 Group 实现聊天室、游戏房间：

```go
// 加入房间
gateway_api.JoinGroup(clientID, "room_123")

// 房间内广播
gateway_api.SendToGroup("room_123", message)

// 离开房间
gateway_api.LeaveGroup(clientID, "room_123")

// 解散房间（所有成员被移出）
gateway_api.Ungroup("room_123")
```

### 3. Session 存储用户状态

```go
// 设置用户信息
gateway_api.SetSession(clientID, map[string]interface{}{
    "name":  "Alice",
    "role":  "admin",
    "level": 5,
})

// 更新部分字段（不影响其他 key）
gateway_api.UpdateSession(clientID, map[string]interface{}{
    "level": 6,  // 只更新 level
})
```

### 4. 定时广播

配合 `GatewayClient` 在独立进程中做定时推送：

```go
client := gateway_client.New(addrs, key)
defer client.Close()

ticker := time.NewTicker(30 * time.Second)
for range ticker.C {
    msg, _ := json.Marshal(map[string]interface{}{
        "type":       "heartbeat",
        "server_time": time.Now().Unix(),
    })
    client.SendToAll(msg)
}
```
