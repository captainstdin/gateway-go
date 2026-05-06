# gateway 包

## 功能

实现 GatewayWorker 架构的 **Gateway 进程**，负责维护客户端连接、协议解析、消息转发。

## 原理

Gateway 是客户端与业务逻辑之间的桥梁，承担所有网络 I/O，将业务处理完全卸载给 Worker。

### 核心职责

```
客户端 ←──(WebSocket/TCP)──→ Gateway ←──(内部协议)──→ Worker
                                ↑
                        维护连接状态:
                        - 连接表 (clientConns)
                        - UID 映射 (uidConns)
                        - 分组映射 (groupConns)
```

### 多协议支持

Gateway 支持同时监听多种协议：

| 协议前缀 | 说明 | 示例 |
|---|---|---|
| `websocket://` / `ws://` | WebSocket | `ws://0.0.0.0:7272` |
| `tcp://` / `frame://` | 4 字节长度头 TCP | `tcp://0.0.0.0:7273` |
| 自定义协议名 | 需提前 `RegisterProtocol` | `text://0.0.0.0:7274` |

通过 `ClientProtocol` 接口实现协议可插拔：

```go
type ClientProtocol interface {
    Input(buf []byte) int        // 粘包分割
    Decode(packet []byte) []byte // 解包
    Encode(data []byte) []byte   // 封包
}
```

### 路由策略

Gateway 通过 Router 选择将客户端消息转发给哪个 Worker：

| 模式 | 说明 |
|---|---|
| `random` | 随机选择（默认） |
| `least_connections` | 最少连接数优先 |

客户端首次消息绑定到某个 Worker 后，后续消息始终发往同一 Worker（连接亲和性）。

### 心跳机制

- **客户端心跳**：可配置 `PingInterval` 和 `PingNotResponseLimit`，超时无响应则断开
- **Worker 心跳**：固定 25 秒间隔，与客户端心跳解耦

### 内部通讯

Gateway 监听一个内部 TCP 端口（`LanIP:LanPort`），Worker 通过此端口连接。所有内部通讯使用 AES-256-CBC 加密 + 4 字节长度头分包。

## 文件结构

| 文件 | 功能 |
|---|---|
| `gateway.go` | 主逻辑：启动、客户端连接管理、心跳、注册 |
| `gateway_worker_handler.go` | 处理 Worker 发来的所有命令（发送、绑定、查询等） |
| `client_conn.go` | 客户端连接抽象接口（WebSocket/TCP 统一） |
| `client_protocol.go` | TCP 协议注册表和接口定义 |
| `protocol_frame.go` | 内置 Frame 协议（4 字节长度头） |
| `protocol_length_field.go` | 内置 LengthField 协议 |
| `protocol_text.go` | 内置 Text 协议（换行符分隔） |
| `router.go` | Worker 路由策略（随机/最少连接） |

## 核心数据结构

```go
type Gateway struct {
    clientConns  map[uint32]*ClientConnection  // 所有客户端连接
    uidConns     map[string]map[uint32]*...     // UID → 连接映射
    groupConns   map[string]map[uint32]*...     // Group → 连接映射
    workerConns  map[string]net.Conn            // Worker 连接
}

type ClientConnection struct {
    ID            uint32
    Conn          ClientConn       // WebSocket 或 TCP
    Session       string           // JSON session
    UID           string           // 绑定的用户 ID
    Groups        map[string]bool  // 所属分组
    BoundWorkerKey string          // 绑定的 Worker
}
```
