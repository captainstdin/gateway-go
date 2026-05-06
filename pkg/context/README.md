# context 包

## 功能

提供 Gateway Worker 架构中的 **请求上下文管理** 和 **客户端标识转换** 工具。

## 核心概念

### Client ID

每个客户端连接由一个 **20 字符的十六进制字符串** 唯一标识，编码规则：

```
client_id = hex(pack(localIP, localPort, connectionID))
         = hex([4字节IP][2字节端口][4字节连接ID])
         = 20 个 hex 字符
```

与 PHP 版 `Context::addressToClientId` / `Context::clientIdToAddress` 完全兼容。

### Session

使用 JSON 编解码的 `map[string]interface{}` 存储每个连接的会话数据。

## 导出函数

| 函数 | 说明 |
|---|---|
| `AddressToClientID(ip, port, connID)` | 将网络地址编码为 client_id |
| `ClientIDToAddress(clientID)` | 将 client_id 解码为网络地址 |
| `SessionEncode(session)` | Session map → JSON 字符串 |
| `SessionDecode(data)` | JSON 字符串 → Session map |

## Context 结构体

```go
type Context struct {
    LocalIP, ClientIP       uint32
    LocalPort, ClientPort   uint16
    ClientID                string
    ConnectionID            uint32
    Session, OldSession     map[string]interface{}
}
```

由 BusinessWorker 在处理每个 Gateway 转发的请求时设置，包含当前请求的完整连接信息。
