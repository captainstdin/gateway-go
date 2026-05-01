# GatewayWorker-Go 使用指南

## 快速开始

### 编译

```bash
cd Gatewayworker-go
go build -o bin/register  ./cmd/register
go build -o bin/gateway   ./cmd/gateway
go build -o bin/worker    ./cmd/worker
go build -o bin/dashboard ./cmd/dashboard
```

### 启动服务

```bash
# 终端1: Register
./bin/register -listen "text://0.0.0.0:1236" -key "my-secret-key"

# 终端2: Gateway（WebSocket + TCP 双协议）
./bin/gateway -listen "websocket://0.0.0.0:7272,tcp://0.0.0.0:7273" -key "my-secret-key" -register "127.0.0.1:1236"

# 终端3: Worker（内置 echo 示例）
./bin/worker -key "my-secret-key" -register "127.0.0.1:1236"

# 终端4: Dashboard（可选）
./bin/dashboard -listen "0.0.0.0:8686" -key "my-secret-key" -register "127.0.0.1:1236"
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
| `websocket://` | WebSocket 协议 | Gateway |
| `ws://` | WebSocket 简写 | Gateway |
| `tcp://` | TCP 4字节长度头协议 | Gateway |

不带前缀时，Register 默认 text，Gateway 默认 websocket。

**Gateway 多协议监听**（逗号分隔）：

```bash
# 同时监听 WebSocket 和 TCP
./gateway -listen "websocket://0.0.0.0:7272,tcp://0.0.0.0:7273" -key "xxx"

# 只开 WebSocket
./gateway -listen "ws://0.0.0.0:7272" -key "xxx"
```

---

## 配置参数

### Register

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `text://0.0.0.0:1236` | 监听地址 |
| `-key` | `""` | 认证密钥（同时用于 AES 加密） |

### Gateway

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `websocket://0.0.0.0:7272` | 监听地址，逗号分隔多个 |
| `-lan-ip` | `127.0.0.1` | 内网 IP（分布式部署时设为本机内网 IP）|
| `-start-port` | `2000` | 内部通讯起始端口 |
| `-id` | `0` | 实例 ID（多实例时需不同）|
| `-register` | `127.0.0.1:1236` | Register 地址，逗号分隔多个 |
| `-key` | `""` | 认证密钥 |
| `-ping-interval` | `55` | 心跳间隔（秒），0 禁用 |
| `-ping-limit` | `0` | 心跳未响应上限，0 不检测 |
| `-router` | `least_connections` | 路由模式：`random` / `least_connections` |

### Worker

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-name` | `worker` | Worker 名称 |
| `-id` | `0` | Worker ID |
| `-register` | `127.0.0.1:1236` | Register 地址，逗号分隔多个 |
| `-key` | `""` | 认证密钥 |

### Dashboard

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-listen` | `0.0.0.0:8686` | Web 监听地址 |
| `-register` | `127.0.0.1:1236` | Register 地址，逗号分隔多个 |
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
    bw := worker.New("chat", 0, []string{"127.0.0.1:1236"}, "my-key")

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

## GatewayClient SDK

用于从 **Worker 外部的其他进程** 向客户端推送消息。

### 典型场景

- HTTP API 收到请求后推送实时通知
- 管理后台踢出某个用户
- 定时任务向所有在线用户广播

### 使用方式

```go
package main

import (
    "fmt"
    "gatewayworker-go/pkg/gateway_client"
)

func main() {
    client := gateway_client.New(
        []string{"127.0.0.1:1236"},
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

### GatewayClient vs gateway_api

| 特性 | `gateway_api` | `GatewayClient` |
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

### 仅 GatewayClient 可用

| 方法 | 说明 |
|------|------|
| `IsOnline(clientID)` | 判断是否在线 |
| `GetAllClientCount()` | 获取在线连接总数 |
| `GetSession(clientID)` | 获取 session |

---

## 分布式部署

### 单机部署

```bash
./register  -listen "text://0.0.0.0:1236" -key "xxx"
./gateway   -listen "ws://0.0.0.0:7272" -key "xxx"
./worker    -key "xxx"
./dashboard -listen "0.0.0.0:8686" -key "xxx"
```

### 多机部署

```bash
# 服务器A: Register
./register -listen "text://0.0.0.0:1236" -key "xxx"

# 服务器B: Gateway
./gateway -listen "ws://0.0.0.0:7272" -lan-ip "192.168.1.2" -key "xxx" \
    -register "192.168.1.1:1236"

# 服务器C: Worker
./worker -key "xxx" -register "192.168.1.1:1236"
```

> **注意**：跨机部署时 Gateway 必须设置 `-lan-ip` 为本机内网 IP。

### 多 Register 高可用

```bash
# 两台 Register
./register -listen "text://0.0.0.0:1236" -key "xxx"   # 服务器A
./register -listen "text://0.0.0.0:1236" -key "xxx"   # 服务器B

# Gateway/Worker 填多个 Register（逗号分隔）
./gateway -listen "ws://0.0.0.0:7272" -key "xxx" \
    -register "192.168.1.10:1236,192.168.1.11:1236"

./worker -key "xxx" -register "192.168.1.10:1236,192.168.1.11:1236"
```

### 多 Gateway 实例

```bash
./gateway -id 0 -listen "ws://0.0.0.0:7272" -key "xxx"   # 内部端口 2000
./gateway -id 1 -listen "ws://0.0.0.0:7273" -key "xxx"   # 内部端口 2001
```

### 多 Worker 实例

```bash
./worker -name "worker" -id 0 -key "xxx"
./worker -name "worker" -id 1 -key "xxx"
```
