# protocol 包

## 功能

定义 Gateway 与 Worker 之间的 **二进制通讯协议**，包括数据结构、命令字常量、编解码函数。

## 协议格式

### 包结构（28 字节头 + 变长数据）

```
 0                   4     5              9    11             15   17             21  22    24             28
 ├───────────────────┼─────┼──────────────┼────┼──────────────┼────┼──────────────┼───┼─────┼──────────────┤
 │    pack_len(4B)   │cmd  │ local_ip(4B) │port│ client_ip(4B)│port│conn_id(4B)   │flg│gw_pt│ ext_len(4B)  │
 │    uint32 BE      │1B   │ uint32 BE    │2B  │ uint32 BE    │2B  │uint32 BE     │1B │2B BE│ uint32 BE    │
 ├───────────────────┴─────┴──────────────┴────┴──────────────┴────┴──────────────┴───┴─────┴──────────────┤
 │                                      ext_data (变长)                                                    │
 ├─────────────────────────────────────────────────────────────────────────────────────────────────────────┤
 │                                      body (变长)                                                        │
 └─────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- **pack_len**: 整包长度（含头部），大端序
- **cmd**: 命令字，标识消息类型
- **local_ip/local_port**: Gateway 内网地址（用于生成 client_id）
- **client_ip/client_port**: 客户端真实地址
- **connection_id**: 连接 ID
- **flag**: 标志位（是否跳过协议 encode 等）
- **ext_len + ext_data**: 扩展数据（session、JSON 参数等）
- **body**: 消息体

### 粘包处理

使用 `Input()` 函数从 TCP 流前 4 字节读取 `pack_len`，确保接收完整包后再解码。

## 命令字常量

### Gateway → Worker（事件通知）

| 常量 | 值 | 说明 |
|---|---|---|
| `CmdOnConnect` | 1 | 新客户端连接 |
| `CmdOnMessage` | 3 | 客户端消息 |
| `CmdOnClose` | 4 | 客户端断开 |
| `CmdOnWebSocketConnect` | 205 | WebSocket 握手完成 |

### Worker → Gateway（指令）

| 常量 | 值 | 说明 |
|---|---|---|
| `CmdSendToOne` | 5 | 发送给单个客户端 |
| `CmdSendToAll` | 6 | 广播 |
| `CmdKick` | 7 | 踢出客户端 |
| `CmdDestroy` | 8 | 直接销毁连接 |
| `CmdBindUID` | 12 | 绑定 UID |
| `CmdUnbindUID` | 13 | 解绑 UID |
| `CmdSendToUID` | 14 | 向 UID 发送 |
| `CmdJoinGroup` | 20 | 加入分组 |
| `CmdLeaveGroup` | 21 | 离开分组 |
| `CmdSendToGroup` | 22 | 向分组发送 |
| `CmdUngroup` | 27 | 解散分组 |
| `CmdSelect` | 25 | 条件查询 |

### 查询类命令

| 常量 | 值 | 说明 |
|---|---|---|
| `CmdIsOnline` | 11 | 判断是否在线 |
| `CmdGetAllClientSessions` | 10 | 获取所有 session |
| `CmdGetClientCountByGroup` | 24 | 获取组在线数 |
| `CmdGetClientIDByUID` | 15 | 获取 UID 绑定的连接 |
| `CmdGetGroupIDList` | 26 | 获取在线组列表 |
| `CmdGetSessionByClientID` | 203 | 获取指定连接 session |

## 导出函数

| 函数 | 说明 |
|---|---|
| `NewEmptyData()` | 创建空的 GatewayData |
| `Encode(data)` | GatewayData → 二进制 |
| `Decode(buf)` | 二进制 → GatewayData |
| `Input(buf)` | TCP 粘包处理，返回包长度 |
