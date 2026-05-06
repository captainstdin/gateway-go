# 开发注意事项

> 使用 gatewayworker-go 开发时需要了解的包大小限制、最佳实践和常见陷阱。

---

## 一、包大小限制

框架在多个层级设置了包大小上限，超限时连接会被断开或报错。

### 限制链路图

```
客户端消息 ─→ Gateway 协议层 ─→ 内部加密传输 ─→ Worker
             ① 10MB              ② 50MB
```

### 各层限制详情

| 位置 | 限制 | 超限行为 | 涉及文件 |
|---|---|---|---|
| ① **客户端协议** | **10MB** | `Input()` 返回 -1，连接被关闭 | `protocol_length_field.go:16` |
| ② **内部通讯** | **50MB** | `readEncryptedPacket` 返回错误，Worker 断开重连 | `gateway.go`, `business_worker.go`, `gateway_conn.go`, `gateway_client.go` |
| ③ **Register 通讯** | **64KB** | `bufio.Scanner` 丢弃超长行 | `register.go:104`, `business_worker.go:156` |

### 常量定义位置

限制值集中定义在两个位置，修改一处即全局生效：

| 常量 | 值 | 定义位置 | 影响范围 |
|---|---|---|---|
| `protocol.MaxEncryptedPacketSize` | 50MB | `pkg/protocol/gateway_protocol.go` | Gateway、Worker、GatewayClient、gateway_api 四个组件的内部加密通讯 |
| `maxPacketSize` | 10MB | `pkg/gateway/protocol_length_field.go` | 面向客户端的 TCP/Frame/Text 协议层 |

> **注意**：这两层限制是独立的。客户端 10MB 限制保护 Gateway 不被外部恶意大包攻击；内部 50MB 限制保护组件间通讯不因异常数据 OOM。内部限制大于客户端限制是因为内部包含额外的协议头、Session 数据和 AES 加密填充。

### 各协议客户端限制

| 协议 | 限制 | 说明 |
|---|---|---|
| `tcp://` (LengthField) | 10MB body | 4 字节头存 body 长度，body > 10MB 断开 |
| `frame://` | 10MB 总包长 | 4 字节头存总长，总长 > 10MB 断开 |
| `text://` | 10MB 单行 | 无换行符且缓冲区 > 10MB 断开 |
| `ws://` (WebSocket) | **无框架限制** | 受 gorilla/websocket 默认限制，需注意 |

### 实际建议

```
推荐单条消息大小: < 1MB（最佳性能和内存表现）
安全上限:         < 5MB（不会触发任何限制）
绝对上限:         < 10MB（超过会被协议层拒绝）
```

> ⚠️ **大消息的隐患不只是限制**：
> - AES 加密需要 PKCS7 填充，10MB 消息加密后还会稍大
> - 广播 10MB 消息给 1000 个客户端 = 10GB 网络 I/O
> - Gateway 会在内存中持有完整消息直到所有客户端写完

---

## 二、Session 使用注意

### Session 大小

Session 以 JSON 字符串形式存储在 Gateway 内存中，并在 **每条消息** 的协议包中传输给 Worker。

```
消息大小 = 28字节头部 + Session长度 + Body长度
```

- ✅ Session 放少量用户标识：`{"uid":"123","role":"admin"}` (~30 bytes)
- ❌ Session 放大量数据：购物车、聊天记录等（每条消息都带着传输，浪费带宽）

### Session 并发

同一 client 的 `SetSession` 和 `UpdateSession` 是通过 Gateway 的写锁保护的，不会有竞态。

但如果在 OnMessage 中读取 session、修改后再 SetSession，在极端高频消息下两次 OnMessage 可能看到同一个旧 session。建议使用 `UpdateSession`（merge）代替 `SetSession`（覆盖）。

---

## 三、回调函数注意事项

### panic 处理

Worker 的回调函数（`OnMessage`、`OnConnect` 等）运行在 Gateway 连接的读取 goroutine 中。**如果回调 panic，该 Gateway 连接会断开重连**，期间所有经过这个 Gateway 的消息都会丢失。

```go
// ❌ 危险：未处理的 panic 会导致连接断开
bw.OnMessage = func(clientID string, msg []byte) {
    var data map[string]interface{}
    json.Unmarshal(msg, &data)
    name := data["name"].(string)  // 如果 name 不存在或不是 string → panic!
}

// ✅ 安全：做好类型检查
bw.OnMessage = func(clientID string, msg []byte) {
    var data map[string]interface{}
    if err := json.Unmarshal(msg, &data); err != nil {
        return
    }
    name, _ := data["name"].(string)  // 安全的类型断言
}
```

### 阻塞风险

OnMessage 回调**不要做长时间阻塞操作**（如 HTTP 请求、数据库慢查询），否则会阻塞该 Gateway 连接上所有后续消息的处理。

```go
// ❌ 阻塞：HTTP 请求可能耗时数秒
bw.OnMessage = func(clientID string, msg []byte) {
    resp, _ := http.Get("https://slow-api.example.com/query")
    // 阻塞期间，该 Gateway 上所有客户端消息都排队等待
}

// ✅ 非阻塞：开 goroutine 异步处理
bw.OnMessage = func(clientID string, msg []byte) {
    go func() {
        resp, _ := http.Get("https://slow-api.example.com/query")
        gateway_api.SendToClient(clientID, processResponse(resp))
    }()
}
```

---

## 四、查询类 API 注意事项

### 死锁已解决

`GetSession`、`IsOnline` 等查询 API 使用独立连接池，在 OnMessage 回调中调用**不会死锁**。

### 性能特性

| API 类型 | 网络开销 | 说明 |
|---|---|---|
| fire-and-forget（Send/Bind/Join） | 极低 | 单向发送，不等待响应 |
| 单点查询（GetSession/IsOnline） | 低 | 查询 1 个 Gateway |
| 全量查询（GetAllClientCount/GetAllUidList） | **高** | 并发查询所有 Gateway |

全量查询在大集群下（10+ Gateway）会产生显著网络开销，不要在热路径中频繁调用：

```go
// ❌ 每条消息都查在线数
bw.OnMessage = func(clientID string, msg []byte) {
    count := gateway_api.GetAllClientCount()  // 每秒数千次全量查询!
    ...
}

// ✅ 用定时器缓存
var onlineCount int32
go func() {
    for range time.Tick(5 * time.Second) {
        atomic.StoreInt32(&onlineCount, int32(gateway_api.GetAllClientCount()))
    }
}()
```

---

## 五、连接数规划

### 单 Gateway 连接数

| 因素 | 估算 |
|---|---|
| goroutine 内存 | ~4KB/连接 |
| ClientConnection 结构 | ~0.5KB/连接 |
| 系统文件描述符 | 需设置 `ulimit -n` |

10 万连接 ≈ 500MB 内存。

### 水平扩展

多 Gateway 部署时，每个 Worker 会连接所有 Gateway：

```
Worker 连接数 = Gateway 数量 × Worker 数量
```

10 个 Gateway + 5 个 Worker = 50 条内部 TCP 连接（很轻量）。

---

## 六、WebSocket 特殊注意

### 消息类型

当前 WebSocket 发送默认使用 `TextMessage`。如果业务需要二进制消息（如 Protobuf），需要注意客户端的解析。

### 连接关闭

WebSocket 客户端断开时，`OnClose` 回调会触发。但如果客户端是非正常断开（如断网），需要等到心跳超时（`PingInterval × PingNotResponseLimit`）后才能检测到。

### 建议的心跳配置

```go
cfg := &gateway.Config{
    PingInterval:         55,  // 55 秒发一次心跳
    PingNotResponseLimit: 1,   // 1 次未响应就断开
    PingData:             "",  // 空 = 服务端不主动发 ping，依赖客户端心跳
}
```
