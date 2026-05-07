# GatewayWorker-Go 架构原理

## 整体架构

GatewayWorker-Go 采用 **Gateway + Worker 分离** 的架构，将网络 IO 与业务逻辑解耦，实现高性能、可横向扩展的实时通讯服务。

```
                         ┌──────────────────┐
                         │     Register     │
                         │    (注册中心)     │
                         └─┬──────┬──────┬──┘
                  注册/发现│      │      │发现Gateway地址
             ┌────────────┘      │      └────────────────┐
             ▼                   │注册/发现               ▼
┌────────────────────────┐       │        ┌──────────────────────┐
│        Gateway         │       │        │     GatewaySDK       │
│  (网关，管理客户端连接)  │       │        │    (独立推送 SDK)     │
└──┬──────────────────┬──┘       │        └──────────┬───────────┘
   │                  │          │                   │
   │ WebSocket/TCP    │ GatewayProtocol+AES          │ GatewayProtocol+AES
   │ (对外客户端)     │ (内部命令端口)                │ (连接内部命令端口)
   ▼                  ▼          ▼                   │
┌────────┐    ┌──────────────────────┐               │
│ Client │    │    BusinessWorker    │◄──────────────┘
│        │    │    (业务逻辑处理)     │  (走同一个内部端口，
└────────┘    └──────────────────────┘   认证方式不同)
```

> **GatewaySDK 本质上是一个"主动型 BusinessWorker"**：它和 BusinessWorker 一样连接 Register 发现 Gateway 地址，然后连接 Gateway 的内部命令端口发送指令。区别在于：
> - BusinessWorker 使用 `CMD_WORKER_CONNECT` 认证，**被动**接收 Gateway 转发的客户端事件
> - GatewaySDK 使用 `CMD_GATEWAY_CLIENT_CONNECT` 认证，**主动**向 Gateway 发送推送/查询等指令

---

## 工作流程

1. **启动 Register** — 监听 TCP 端口，等待 Gateway 和 Worker 注册
2. **启动 Gateway** — 向 Register 注册自己的内部通讯地址，同时监听客户端连接（WebSocket/TCP）
3. **启动 Worker** — 向 Register 注册，Register 回复所有 Gateway 地址，Worker 连接所有 Gateway
4. **客户端连接** — 客户端通过 WebSocket 或 TCP 连接到 Gateway
5. **消息转发** — Gateway 收到客户端消息后，通过内部协议转发给 Worker
6. **业务处理** — Worker 执行业务逻辑（用户编写的回调函数），通过 API 向客户端发送响应

### 消息流转详细过程

```
客户端发送消息:
  Client --[WebSocket/TCP]--> Gateway --[GatewayProtocol+AES]--> Worker
                                                                  │
                                                           OnMessage(clientID, msg)
                                                                  │
                                                        gateway_api.SendToClient()
                                                                  │
  Client <--[WebSocket/TCP]-- Gateway <--[GatewayProtocol+AES]----┘
```

---

## 为什么分离 Gateway 和 Worker？

| 优势 | 说明 |
|------|------|
| **热更新** | 重启 Worker 不影响客户端连接，Gateway 维持长连接 |
| **横向扩展** | Gateway 和 Worker 可独立扩容，按需分配资源 |
| **职责分离** | Gateway 专注 IO，Worker 专注业务，互不干扰 |
| **分布式部署** | 三个组件可部署在不同服务器上 |

---

## 五个组件详解

### Register（注册中心）

- **职责**：维护 Gateway 和 Worker 的地址列表，当 Gateway 上线/下线时通知所有 Worker
- **协议**：文本协议（JSON + AES加密 + Base64编码 + `\n` 分隔）
- **监听地址**：支持 URI 格式，如 `text://0.0.0.0:51234`
- **无状态**：不存储业务数据，仅做服务发现和状态中转
- **高可用**：支持部署**多个 Register 实例**，Gateway/Worker 同时连接所有 Register，任一 Register 存活即可正常工作

**支持的事件**：

| 事件 | 发送方 | 说明 |
|------|-------|------|
| `gateway_connect` | Gateway | Gateway 注册，携带内部通讯地址 |
| `worker_connect` | Worker | Worker 注册，携带 worker 名称 |
| `worker_stats` | Worker | Worker 每 3 秒上报处理计数 |
| `admin_connect` | Dashboard | 管理后台连接，订阅实时状态推送 |
| `ping` | 任意 | 心跳保活 |
| `broadcast_addresses` | Register | 广播 Gateway 地址列表给 Worker |
| `status_update` | Register | 推送完整状态（gateways+workers）给 Admin |

**多 Register 高可用原理**：

```
Register-A (192.168.1.10:51234)     Register-B (192.168.1.11:51234)
     ▲    ▲                              ▲    ▲
     │    └──── Worker ──────────────────┘    │
     │                                        │
     └──────── Gateway ──────────────────────┘

• 每个 Gateway 向所有 Register 注册（独立连接）
• 每个 Worker  连接所有 Register 接收广播
• Register 之间互不通信，各自独立维护地址列表
• 任一 Register 宕机，其余 Register 继续提供服务发现
• 所有 Register 都宕机时，已建立的 Gateway⇔Worker 连接不受影响
```

### Gateway（网关）

Gateway 维护两类连接：

**对外连接（客户端）**：
- 同时支持 **WebSocket** 和 **TCP**（4字节长度头协议）
- 监听地址支持 URI 格式：`ws://0.0.0.0:7272`（明文）、`wss://0.0.0.0:7443`（TLS）、`tcp://0.0.0.0:7273`
- 支持逗号分隔同时监听多个地址
- 为每个客户端分配全局唯一的 `connection_id`（uint32，最大约 42.9 亿）
- 维护 session、UID 绑定、Group 分组等状态

**对内连接（Worker/GatewaySDK）**：
- 监听内部 TCP 端口（`lanIP:startPort+instanceID`），使用 GatewayProtocol + AES
- 接收 Worker/GatewaySDK 的连接和指令（发消息、踢人、绑定UID、加入Group 等 30+ 种命令）

**路由策略**：
- `least_connections`（默认）：将客户端路由到连接数最少的 Worker，自动负载均衡
- `random`：随机选择 Worker
- 绑定模式：同一客户端的所有消息始终路由到同一个 Worker

### BusinessWorker（业务处理器）

- 启动后连接 Register 获取所有 Gateway 地址
- 与每个 Gateway 建立 TCP 长连接
- 接收 Gateway 转发的事件，调用用户回调
- 内置处理计数器（`uint64`），每处理一条消息自增
- 每 3 秒向 Register 上报处理统计（`worker_stats` 事件）
- 与 Gateway 或 Register 断线后自动重连

### GatewaySDK（独立推送 SDK）

- 不依赖 Worker 进程，可从任意 Go 程序使用
- 连接 Register 发现 Gateway 地址（带 1 秒缓存）
- 使用连接池复用到 Gateway 的 TCP 连接（50 秒 TTL）
- 使用 `CMD_GATEWAY_CLIENT_CONNECT` 命令认证（区别于 Worker 的 `CMD_WORKER_CONNECT`）

### Dashboard（监控面板）

- 独立 web 程序，提供实时可视化监控
- 以 `admin_connect` 身份连接 Register，订阅状态变更推送
- 每 3 秒轮询各 Gateway 获取在线连接数
- 通过 WebSocket 将数据实时推送到浏览器
- 展示内容：Gateway 列表 + 在线连接数、Worker 列表 + 累计处理次数

---

## 通讯协议

### 客户端协议

**WebSocket**：标准 WebSocket 协议，消息直接作为帧收发。

**TCP**：4 字节长度前缀协议：

```
┌────────────────────┬──────────────────────┐
│ 4字节 pack_len (BE)│  pack_len 字节 body  │
└────────────────────┴──────────────────────┘
```

### GatewayProtocol（内部二进制协议）

Gateway 与 Worker 间的通讯协议，28 字节固定头部 + 变长数据，全部 Big-Endian：

```
偏移  大小  字段            说明
─────────────────────────────────────
 0    4    pack_len        整包长度
 4    1    cmd             命令字
 5    4    local_ip        Gateway 内网 IP
 9    2    local_port      Gateway 内部端口
11    4    client_ip       客户端 IP
15    2    client_port     客户端端口
17    4    connection_id   连接 ID
21    1    flag            标志位
22    2    gateway_port    Gateway 对外端口
24    4    ext_len         ext_data 长度
28    变长  ext_data        扩展数据 (session等)
28+N  变长  body            包体
```

### 命令字一览

| 命令 | 值 | 方向 | 说明 |
|------|---|------|------|
| CMD_ON_CONNECT | 1 | G→W | 新客户端连接 |
| CMD_ON_MESSAGE | 3 | G→W | 客户端消息 |
| CMD_ON_CLOSE | 4 | G→W | 客户端断开 |
| CMD_SEND_TO_ONE | 5 | W→G | 发给单个客户端 |
| CMD_SEND_TO_ALL | 6 | W→G | 广播 |
| CMD_KICK | 7 | W→G | 踢出客户端 |
| CMD_DESTROY | 8 | W→G | 销毁连接 |
| CMD_UPDATE_SESSION | 9 | W→G | 合并 session |
| CMD_BIND_UID | 12 | W→G | 绑定 UID |
| CMD_UNBIND_UID | 13 | W→G | 解绑 UID |
| CMD_SEND_TO_UID | 14 | W→G | 向 UID 发送 |
| CMD_JOIN_GROUP | 20 | W→G | 加入分组 |
| CMD_LEAVE_GROUP | 21 | W→G | 离开分组 |
| CMD_SEND_TO_GROUP | 22 | W→G | 向分组发送 |
| CMD_UNGROUP | 27 | W→G | 解散分组 |
| CMD_WORKER_CONNECT | 200 | W→G | Worker 认证 |
| CMD_PING | 201 | 双向 | 心跳 |
| CMD_GATEWAY_CLIENT_CONNECT | 202 | C→G | GatewaySDK 认证 |
| CMD_ON_WEBSOCKET_CONNECT | 205 | G→W | WS 握手完成 |

### Client ID 编码

每个客户端有一个全局唯一的 **20 字符 hex** 标识符（client_id），编码规则：

```
client_id = hex( uint32(local_ip) + uint16(local_port) + uint32(connection_id) )
            ──── 4字节 ────────── ─── 2字节 ──────────── ──── 4字节 ────────────
            = 10字节二进制 → 20字符十六进制
```

通过 client_id 可以直接解码出所在 Gateway 的地址，实现精确寻址。

---

## 安全机制

所有内部通讯均使用 **AES-256-CBC** 加密 + **SecretKey** 认证双重保护：

### 密钥派生

```
AES Key = SHA256(SecretKey) → 32 字节
```

### Register 通讯加密

```
发送: JSON → AES-256-CBC加密(随机IV) → Base64编码 → 追加\n → TCP发送
接收: 按\n分隔 → Base64解码 → AES-256-CBC解密 → JSON解析
```

### Gateway⇔Worker 通讯加密

```
发送: GatewayProtocol.Encode() → AES-256-CBC加密 → 前置4字节(密文长度) → TCP发送
接收: 读4字节长度 → 读密文 → AES-256-CBC解密 → GatewayProtocol.Decode()
```

### 认证流程

```
Gateway → Register:  {"event":"gateway_connect", "secret_key":"xxx", "address":"ip:port"}
Worker  → Register:  {"event":"worker_connect", "secret_key":"xxx", "name":"worker:0"}
Admin   → Register:  {"event":"admin_connect", "secret_key":"xxx"}
Worker  → Register:  {"event":"worker_stats", "name":"worker:0", "processed_count":12345}
Worker  → Gateway:   CMD_WORKER_CONNECT, body={"worker_key":"name:id", "secret_key":"xxx"}
Client  → Gateway:   CMD_GATEWAY_CLIENT_CONNECT, body={"secret_key":"xxx"}
```

Register 和 Gateway 均会验证 secret_key，不匹配则立即断开连接。
