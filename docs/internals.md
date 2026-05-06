# 内部设计决策与关键实现备忘

> 记录那些"代码注释说不清楚、architecture.md 又太宏观"的关键设计决策和实现细节。
> 后续开发者（包括 AI）在修改代码前务必阅读，避免重复踩坑。

---

## 一、为什么有两个 API 包？

```
gateway_api  — Worker 进程内部使用（包级函数）
gateway_sdk  — 外部进程使用（实例方法）
```

PHP 版只有一个 `Gateway` 类，通过 `static::$businessWorker` 是否存在来判断当前环境。Go 版拆分的原因：

1. **Go 是静态语言**，不能运行时切换 static 行为
2. **职责清晰**：`gateway_api` 复用 Worker 连接（零额外开销），`gateway_sdk` 自建连接池（独立生命周期）
3. **避免循环依赖**：`gateway_api` 依赖 `worker`，`gateway_sdk` 不依赖任何框架包

> ⚠️ 两个包的**协议命令完全相同**，只是传输通道不同。不要为它们维护两套命令处理逻辑。

---

## 二、查询连接池（queryConnPool）为什么存在

**问题**：Worker 回调（OnMessage 等）运行在 Gateway 连接的读取 goroutine 中。如果在回调中通过同一连接发送查询请求并等待响应，读取循环被阻塞，响应永远收不到 → **死锁**。

**解决**：`gateway_api` 包有两套连接：
- **事件连接**（BusinessWorker 持有）→ 只发 fire-and-forget 命令（Send/Bind/Join/Kick 等）
- **查询连接**（queryConnPool）→ 发请求等响应（GetSession/IsOnline/GetAllClientCount 等）

两套连接使用相同的 `CmdGatewayClientConnect` 认证，但查询连接是独立的 TCP 连接。

> 📁 实现文件：`pkg/gateway_api/gateway_conn.go`

---

## 三、锁的设计原则

### 绝对不能做的事

1. **持锁做网络 I/O**（已修复多次）
   - 锁内只做内存操作（快照/拷贝）
   - 锁外做 Write/Dial/Read
   - 典型模式：`RLock → 复制列表 → RUnlock → 遍历发送`

2. **在 RLock 回调中调用需要 Lock 的方法**
   - `handleSendToAll` 持 RLock 遍历 → 调用 `sendEncrypted` → 如果 sendEncrypted 内部需要 Lock → 死锁

### Gateway 核心锁

```go
g.mu sync.RWMutex  // 保护 clientConns, workerConns, uidMap, groupMap
```

- **读锁（RLock）**：查询、遍历发送（先快照再发）
- **写锁（Lock）**：连接增删、UID/Group 绑定变更
- **不持锁**：所有网络 Write 操作

### GatewaySDK 连接池锁

```go
c.mu sync.Mutex  // 保护 connPool, addrCache
```

- `getConn`: Lock（读+可能写）
- `getGatewayAddresses`: Lock 检查缓存 → Unlock → 网络 I/O → Lock 更新缓存
- `evictConn`: Lock → Close + delete → Unlock

---

## 四、Client ID 编码是全局路由键

```
client_id = hex(local_ip[4B] + local_port[2B] + connection_id[4B]) = 20 字符
```

这不只是标识符，而是**路由地址**。通过解码 client_id 可以直接定位到具体的 Gateway 实例和连接。所以：

- `SendToClient` 不需要广播，直接定向发送
- `GetSession` 只查询一个 Gateway
- `SendToAll/SendToUID/SendToGroup` 需要广播到所有 Gateway

> ⚠️ **connection_id 是 uint32**，上限 ~42.9 亿。单 Gateway 实例不会溢出，但长期运行需要关注回绕。

---

## 五、加密通讯的分层

```
外部客户端 → Gateway    : WebSocket/TCP 明文协议（仅应用层协议编解码）
Gateway ↔ Worker       : GatewayProtocol 二进制 + AES-256-CBC 全包加密
Gateway ↔ GatewaySDK   : 同上
所有组件 ↔ Register     : JSON + AES-256-CBC + Base64 + \n 文本行协议
```

### 密钥

所有组件使用同一个 `-key` 参数，派生方式：

```go
aesKey = sha256(secretKey)  // 32 字节，直接作为 AES-256 密钥
```

### 加密包格式

```
[4B 密文长度 BigEndian] [密文]
```

密文 = AES-256-CBC(随机 IV + GatewayProtocol 编码数据)

### 包大小限制

两层独立常量：

| 常量 | 值 | 保护对象 |
|---|---|---|
| `protocol.MaxEncryptedPacketSize` | 50MB | 内部组件间通讯 |
| `maxPacketSize`（gateway 包内） | 10MB | 外部客户端协议 |

---

## 六、心跳机制

### Gateway → 客户端

配置 `PingInterval` + `PingNotResponseLimit`：

- `PingInterval > 0` 且 `PingData != ""` → Gateway 主动发 ping，客户端需回复
- `PingInterval > 0` 且 `PingData == ""` → Gateway 不发 ping，但检测客户端是否有消息
- `PingInterval == 0` → 禁用心跳

### Gateway ↔ Worker

Worker 每 25 秒发一次 `CMD_PING`，Gateway 回复 `CMD_PING`。

> 📁 `pkg/gateway/gateway.go` 的 `pingWorkerLoop` 使用预编码的心跳包，避免循环内重复分配。

---

## 七、Worker 路由策略

Gateway 收到客户端消息后，选择一个 Worker 转发：

- **`least_connections`**（默认）：选连接数最少的 Worker
- **`random`**：随机选择

路由决策发生在 Gateway 端，Worker 无感知。

> ⚠️ 同一客户端的不同消息可能路由到不同 Worker（无亲和性），所以业务状态不要放在 Worker 内存中，用 Session 或外部存储。

---

## 八、项目目录结构

```
cmd/
├── register/      # Register 注册中心入口
├── gateway/       # Gateway 网关入口
├── worker/        # Worker 业务处理入口（内置 echo 示例）
├── gateway-edit/  # 自定义协议示例（JsonNL）
├── dashboard/     # Web 监控面板入口
├── test-ws-tui/   # WebSocket TUI 测试客户端
├── tui-ws-chat/   # TUI 聊天客户端
├── build_all.sh   # 一键编译所有组件
└── start.sh       # 一键启动 register + gateway + worker

pkg/
├── register/      # Register 实现
├── gateway/       # Gateway 核心（连接管理、协议、Worker 通讯）
├── worker/        # BusinessWorker 实现
├── gateway_api/   # Worker 内部 API（包级函数）
├── gateway_sdk/   # 外部 SDK（GatewaySDK 结构体）
├── protocol/      # GatewayProtocol 二进制协议 + 命令常量
├── context/       # Client ID 编解码、Session 序列化
└── crypto/        # AES-256-CBC 加解密工具
```

---

## 九、已知的设计约束与后续方向

### 当前约束

1. **单 Worker 单 goroutine 处理一个 Gateway 的消息**：每个 Gateway 连接在 Worker 中是一个读循环 goroutine，消息串行处理。如果回调阻塞，该 Gateway 上所有客户端的消息都排队。
   - 解决方式：回调中用 `go func(){}()` 异步处理

2. **Session 存储在 Gateway 内存**：不支持持久化。Gateway 重启后 Session 丢失。
   - 后续方向：可接 Redis/etcd 存储

3. **Dashboard 状态依赖内存**：统计数据非持久化。
   - 后续方向：可接 Prometheus 指标导出

4. **无 TLS 支持**：当前 WebSocket/TCP 客户端连接是明文。
   - 可在前端加 Nginx/Caddy 做 TLS 终止

### 后续可扩展方向

- Protobuf 替换 JSON 序列化（Session、ExtData）
- Prometheus metrics 导出（连接数、消息 QPS、延迟 P99）
- 连接数限流（单 IP / 全局）
- 消息队列集成（Kafka/NATS 作为 Worker 替代品）
