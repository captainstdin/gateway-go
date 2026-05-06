# gateway_client 包

## 功能

提供从 **外部应用**（非 Worker 进程）直接调用 Gateway API 的客户端。

对应 PHP 版在**非 workerman 环境**中使用 `Gateway` 类的场景（如在 HTTP 控制器、定时任务中向客户端推送消息）。

## 原理

### 使用场景

```
┌──────────────────┐
│   HTTP API 服务   │ ← 收到 REST 请求
│  (独立进程/服务)   │
│                  │
│  client.SendToUID("user123", msg)
│       ↓                         
│  GatewayClient ──→ Register ──→ 获取 Gateway 地址
│       │                         
│       └──→ Gateway-1 (直连 TCP)
│       └──→ Gateway-2 (直连 TCP)
└──────────────────┘
```

与 `gateway_api` 包的区别：

| | gateway_api | gateway_client |
|---|---|---|
| 运行环境 | Worker 进程内部 | 任意外部进程 |
| Gateway 地址来源 | 通过 BusinessWorker | 自行连接 Register 获取 |
| 连接类型 | 复用 Worker 连接 + 独立查询池 | 全部独立连接 |
| 认证方式 | 通过 Worker 已认证 | `CmdGatewayClientConnect` |

### 连接管理

- 通过 Register 获取 Gateway 地址（1 秒缓存）
- 到每个 Gateway 的连接带 50 秒 TTL 缓存
- 所有通讯 AES-256-CBC 加密

## API 方法

| 方法 | 说明 |
|---|---|
| `SendToClient(clientID, message)` | 向指定客户端发送 |
| `SendToAll(message)` | 广播 |
| `SendToUID(uid, message)` | 向 UID 发送 |
| `SendToGroup(group, message)` | 向分组发送 |
| `BindUID(clientID, uid)` | 绑定 UID |
| `UnbindUID(clientID, uid)` | 解绑 UID |
| `JoinGroup(clientID, group)` | 加入分组 |
| `LeaveGroup(clientID, group)` | 离开分组 |
| `CloseClient(clientID, message)` | 踢出客户端 |
| `DestroyClient(clientID)` | 销毁连接 |
| `SetSession(clientID, session)` | 设置 session |
| `UpdateSession(clientID, session)` | 更新 session |
| `IsOnline(clientID)` | 判断是否在线 |
| `GetAllClientCount()` | 获取在线总数 |

## 使用示例

```go
client := gateway_client.New(
    []string{"127.0.0.1:1236"},  // Register 地址
    "your-secret-key",
)
defer client.Close()

// 向某个用户推送消息
client.SendToUID("user123", []byte(`{"type":"notification","msg":"hello"}`))

// 检查用户是否在线
online, _ := client.IsOnline(clientID)
```
