# gateway_api 包

## 功能

提供 **Worker 进程中调用的 Gateway API**，用于向客户端发送消息、管理连接、查询状态。

对应 PHP 版的 `GatewayWorker\Lib\Gateway` 类。

## 原理

### 双连接架构

```
                    ┌─────────────────────────────┐
                    │         Worker 进程           │
                    │                              │
OnMessage 回调 ─────┤  gateway_api.SendToClient()  │── BusinessWorker 事件连接 ──→ Gateway
                    │  gateway_api.BindUID()       │   (fire-and-forget 命令)
                    │                              │
                    │  gateway_api.GetSession()    │── queryConnPool 独立连接 ──→ Gateway
                    │  gateway_api.IsOnline()      │   (请求-响应 查询)
                    └─────────────────────────────┘
```

**为什么需要两套连接？**

- BusinessWorker 的读取循环在处理事件回调时会**阻塞**
- 如果在回调中通过同一连接发送查询并等待响应，会**死锁**
- 查询连接池（`queryConnPool`）使用独立的 TCP 连接，与事件流完全隔离

### 查询连接池

- 文件：`gateway_conn.go`
- 使用 `CmdGatewayClientConnect` 认证
- 50 秒 TTL 连接缓存
- **Go 并发优势**：`queryAllGateways()` 使用 goroutine + WaitGroup 并发查询所有 Gateway

## API 分类

### 发送类

| 函数 | 说明 |
|---|---|
| `SendToClient(clientID, message)` | 向指定客户端发送 |
| `SendToCurrentClient(clientID, message)` | 同 SendToClient |
| `SendToAll(message, clientIDs, excludeIDs)` | 广播（可指定/排除） |
| `SendToUID(uids, message)` | 向 UID 发送 |
| `SendToGroup(groups, message, excludeIDs)` | 向分组发送（可排除） |

### 绑定/分组

| 函数 | 说明 |
|---|---|
| `BindUID(clientID, uid)` | 绑定 UID |
| `UnbindUID(clientID, uid)` | 解绑 UID |
| `JoinGroup(clientID, group)` | 加入分组 |
| `LeaveGroup(clientID, group)` | 离开分组 |
| `Ungroup(group)` | 解散分组 |

### Session

| 函数 | 说明 |
|---|---|
| `SetSession(clientID, session)` | 覆盖 session |
| `UpdateSession(clientID, session)` | 合并 session |
| `GetSession(clientID)` | 获取 session 🔍 |

### 关闭/销毁

| 函数 | 说明 |
|---|---|
| `CloseClient(clientID, message)` | 踢出客户端（可附消息） |
| `CloseCurrentClient(clientID, message)` | 同 CloseClient |
| `DestroyClient(clientID)` | 直接销毁连接 |
| `DestroyCurrentClient(clientID)` | 同 DestroyClient |

### 在线状态查询 🔍

| 函数 | 说明 |
|---|---|
| `IsOnline(clientID)` | 判断连接是否在线 |
| `IsUidOnline(uid)` | 判断 UID 是否在线 |

### 数量统计 🔍

| 函数 | 说明 |
|---|---|
| `GetAllClientCount()` | 总在线连接数 |
| `GetClientCountByGroup(group)` | 分组在线数 |
| `GetUidCountByGroup(group)` | 分组 UID 数 |
| `GetAllUidCount()` | 总在线 UID 数 |
| `GetAllGroupClientIdCount()` | 各组连接数 |
| `GetAllGroupUidCount()` | 各组 UID 数 |

### 列表查询 🔍

| 函数 | 说明 |
|---|---|
| `GetAllClientSessions(group)` | 所有/组内 session |
| `GetClientSessionsByGroup(group)` | 按组获取 session |
| `GetAllClientIdList()` | 所有 client_id |
| `GetClientIdListByGroup(group)` | 组内 client_id |
| `GetClientIdByUid(uid)` | UID 绑定的 client_id |
| `GetClientIdByUids(uids)` | 批量获取 |
| `GetUidListByGroup(group)` | 组内 UID |
| `GetAllUidList()` | 所有在线 UID |
| `GetUidByClientId(clientID)` | 通过 client_id 获取 UID |
| `GetAllGroupIdList()` | 所有组 ID |
| `GetAllGroupUidList()` | 所有组的 UID 列表 |
| `GetAllGroupClientIdList()` | 所有组的 client_id 列表 |

> 🔍 标记的方法使用独立查询连接池，需要等待 Gateway 响应。
