# register 包

## 功能

实现 GatewayWorker 架构的 **注册中心**，负责服务发现和组件协调。

## 原理

Register 是整个架构的中枢，维护 Gateway 和 Worker 的注册信息，实现自动服务发现。

### 架构位置

```
          ┌──────────┐
          │ Register │  ← 注册中心
          └────┬─────┘
        ┌──────┴──────┐
        ↕             ↕
  ┌──────────┐  ┌──────────┐
  │ Gateway  │  │  Worker  │
  │ (连接前端)│←→│ (业务逻辑)│
  └──────────┘  └──────────┘
```

### 工作流程

1. **Gateway 启动** → 连接 Register，上报内部通讯地址
2. **Worker 启动** → 连接 Register，获取所有 Gateway 地址
3. **Register 广播** → 当 Gateway 上下线时，通知所有 Worker 更新连接
4. **Admin 监控** → Dashboard 连接 Register，实时获取集群状态

### 通讯协议

使用 **文本行协议**（每行一个 Base64 加密的 JSON 消息），通过 `\n` 分隔：

```
加密消息格式: Base64(AES-256-CBC(JSON)) + "\n"
```

### 消息事件

| 事件 | 方向 | 说明 |
|---|---|---|
| `gateway_connect` | Gateway → Register | Gateway 注册，携带内部通讯地址 |
| `worker_connect` | Worker → Register | Worker 注册 |
| `worker_stats` | Worker → Register | Worker 上报处理计数 |
| `admin_connect` | Admin → Register | Dashboard 连接 |
| `broadcast_addresses` | Register → Worker | 广播所有 Gateway 地址 |
| `status_update` | Register → Admin | 推送集群状态 |
| `ping` | 任意 → Register | 心跳保活 |

## 安全机制

- 所有消息均 AES-256-CBC 加密传输
- 连接后 10 秒内必须完成认证（`SecretKey` 校验），否则断开
- Gateway/Worker/Admin 三种角色分别管理

## 核心结构

```go
type Register struct {
    ListenAddr          string
    SecretKey           string
    gatewayConnections  sync.Map  // Gateway 内部地址
    workerConnections   sync.Map  // Worker 连接
    adminConnections    sync.Map  // Dashboard 连接
    workerInfos         sync.Map  // Worker 统计信息
}
```
