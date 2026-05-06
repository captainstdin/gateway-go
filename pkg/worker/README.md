# worker 包

## 功能

实现 GatewayWorker 架构的 **BusinessWorker 进程**，负责处理所有业务逻辑。

## 原理

Worker 是纯业务层，不直接与客户端通讯。它通过 Gateway 转发的协议包接收客户端事件，通过 `gateway_api` 包向客户端发送响应。

### 工作流程

```
1. Worker 启动 → 连接 Register 获取 Gateway 地址
2. Worker → 连接所有 Gateway（TCP，AES 加密）
3. Gateway 转发客户端事件 → Worker 处理业务
4. Worker 调用 gateway_api → 通过 Gateway 向客户端发送响应
```

### 连接管理

```
                Register
                  │ (广播 Gateway 地址)
                  ↓
Worker ──────→ Gateway-1  (长连接，自动重连)
       ──────→ Gateway-2
       ──────→ Gateway-N
```

- 从 Register 获取所有 Gateway 内部通讯地址
- 对每个 Gateway 建立独立的 TCP 长连接
- 连接断开后自动重连（1 秒重试）
- 定时上报处理统计信息到 Register

### 事件回调

```go
type BusinessWorker struct {
    OnWorkerStart      func()                              // Worker 启动
    OnWorkerStop       func()                              // Worker 停止
    OnConnect          func(clientID string)                // 客户端连接
    OnMessage          func(clientID string, message []byte) // 客户端消息
    OnClose            func(clientID string)                // 客户端断开
    OnWebSocketConnect func(clientID string, data []byte)   // WebSocket 握手
}
```

### 认证流程

Worker 连接 Gateway 后发送 `CmdWorkerConnect` 认证包：

```json
{"worker_key": "workerName:id", "secret_key": "xxx"}
```

Gateway 验证 `secret_key` 后建立信任通道。

### 心跳与统计

- **Register 心跳**：25 秒间隔
- **统计上报**：3 秒间隔，上报 `processed_count`（累计处理消息数）

## 导出方法

| 方法 | 说明 |
|---|---|
| `New(name, id, registerAddr, secretKey)` | 创建 Worker |
| `Run()` | 启动 Worker |
| `Stop()` | 停止 Worker |
| `SendToGateway(addr, data)` | 向指定 Gateway 发送数据 |
| `SendToAllGateways(data)` | 向所有 Gateway 发送数据 |
| `GetAllGatewayAddresses()` | 获取所有 Gateway 地址 |
| `GetProcessedCount()` | 获取累计处理消息数 |
| `ResetStats()` | 重置统计计数器 |
