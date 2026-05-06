# GatewaySDK SDK 文档

> 从任意 Go 进程（HTTP 服务、定时任务、管理后台等）向在线客户端推送消息、查询状态。

---

## 概述

`gateway_sdk` 包是 **外部进程** 与 GatewayWorker 集群通讯的 SDK。它通过 Register 发现 Gateway 地址，建立独立的 TCP 连接池，支持所有推送、查询、管理操作。

### 与 gateway_api 的区别

| 维度 | `gateway_api`（Worker 内部） | `gateway_sdk`（外部 SDK） |
|---|---|---|
| 使用位置 | Worker 的 OnMessage 等回调中 | 任意 Go 进程 |
| 连接方式 | 复用 Worker 已有连接 | 自建连接池 + Register 发现 |
| 调用方式 | 包级函数 `gateway_api.SendToClient(...)` | 实例方法 `client.SendToClient(...)` |
| 适用场景 | 事件驱动响应 | HTTP 推送、定时任务、管理操作 |
| 并发查询 | ✅ goroutine 并发 | ✅ goroutine 并发 |

### 对应 PHP 版

Go 的 `gateway_sdk` 包等同于 PHP 的 `GatewaySDK\Gateway` 类（[GatewaySDK](https://github.com/walkor/GatewaySDK)），用于在 Worker 外部调用 Gateway API。

---

## 快速开始

### 安装

```go
import "gatewayworker-go/pkg/gateway_sdk"
```

### 基本用法

```go
package main

import (
    "fmt"
    "gatewayworker-go/pkg/gateway_sdk"
)

func main() {
    // 创建客户端（连接 Register 获取 Gateway 地址）
    client := gateway_sdk.New(
        []string{"127.0.0.1:51234"},  // Register 地址
        "my-secret-key",               // 密钥（需与集群一致）
    )
    defer client.Close()

    // 推送消息
    client.SendToAll([]byte(`{"type":"announcement","msg":"系统维护通知"}`))

    // 查询在线人数
    count, _ := client.GetAllClientCount()
    fmt.Println("在线人数:", count)
}
```

### 在 HTTP 服务中使用

```go
package main

import (
    "encoding/json"
    "gatewayworker-go/pkg/gateway_sdk"
    "net/http"
)

var gwClient *gateway_sdk.GatewaySDK

func main() {
    gwClient = gateway_sdk.New(
        []string{"127.0.0.1:51234"}, "my-secret-key",
    )
    defer gwClient.Close()

    http.HandleFunc("/api/notify", handleNotify)
    http.HandleFunc("/api/kick", handleKick)
    http.HandleFunc("/api/online", handleOnline)
    http.ListenAndServe(":8080", nil)
}

func handleNotify(w http.ResponseWriter, r *http.Request) {
    uid := r.URL.Query().Get("uid")
    msg := r.URL.Query().Get("msg")
    gwClient.SendToUID(uid, []byte(msg))
    w.Write([]byte("ok"))
}

func handleKick(w http.ResponseWriter, r *http.Request) {
    clientID := r.URL.Query().Get("client_id")
    gwClient.CloseClient(clientID, []byte("被管理员踢出"))
    w.Write([]byte("ok"))
}

func handleOnline(w http.ResponseWriter, r *http.Request) {
    count, _ := gwClient.GetAllClientCount()
    json.NewEncoder(w).Encode(map[string]int{"online": count})
}
```

---

## 完整 API 参考

### 初始化 & 生命周期

```go
// 创建客户端
client := gateway_sdk.New(registerAddrs []string, secretKey string) *GatewaySDK

// 关闭所有连接
client.Close()
```

---

### 消息发送

| 方法 | 说明 |
|---|---|
| `SendToClient(clientID, msg)` | 向指定客户端发送 |
| `SendToAll(msg)` | 广播给所有客户端 |
| `SendToUID(uid, msg)` | 向指定 UID 发送（uid 可以是 string 或 []interface{} 批量） |
| `SendToGroup(group, msg)` | 向指定分组发送（group 可以是 string 或 []interface{} 批量） |

```go
// 发送给单个客户端
client.SendToClient("7f000001d4310000001", []byte("hello"))

// 广播
client.SendToAll([]byte(`{"type":"broadcast","msg":"大家好"}`))

// 发送给 UID
client.SendToUID("user_100", []byte(`{"type":"msg","content":"hi"}`))

// 发送给分组
client.SendToGroup("room_1", []byte(`{"type":"chat","msg":"hello room"}`))
```

---

### UID 操作

| 方法 | 说明 |
|---|---|
| `BindUID(clientID, uid)` | 绑定 UID（一个 uid 可绑多个 client） |
| `UnbindUID(clientID, uid)` | 解绑 |
| `IsUidOnline(uid)` | 判断 uid 是否在线 → `(bool, error)` |
| `GetClientIdByUid(uid)` | 获取 uid 绑定的所有 client_id → `([]string, error)` |
| `GetUidByClientId(clientID)` | 通过 client_id 获取 uid → `(string, error)` |
| `GetAllUidList()` | 所有在线 uid 列表 → `([]string, error)` |
| `GetAllUidCount()` | 所有在线 uid 数量 → `(int, error)` |
| `GetUidListByGroup(group)` | 分组内在线 uid 列表 → `([]string, error)` |
| `GetUidCountByGroup(group)` | 分组内在线 uid 数量 → `(int, error)` |

---

### Group 操作

| 方法 | 说明 |
|---|---|
| `JoinGroup(clientID, group)` | 加入分组 |
| `LeaveGroup(clientID, group)` | 离开分组 |
| `Ungroup(group)` | 解散分组（所有成员离开） |
| `GetAllGroupIdList()` | 所有在线分组 ID → `([]string, error)` |
| `GetClientIdListByGroup(group)` | 分组成员 client_id 列表 → `([]string, error)` |
| `GetClientCountByGroup(group)` | 分组在线连接数 → `(int, error)` |

---

### Session 操作

| 方法 | 说明 |
|---|---|
| `SetSession(clientID, session)` | 覆盖 session（map[string]interface{}） |
| `UpdateSession(clientID, session)` | 合并 session |
| `GetSession(clientID)` | 获取 session → `(map[string]interface{}, error)` |
| `GetAllClientSessions()` | 所有客户端 session → `(map[clientID]session, error)` |
| `GetClientSessionsByGroup(group)` | 分组成员 session → `(map[clientID]session, error)` |

---

### 连接管理 & 查询

| 方法 | 说明 |
|---|---|
| `CloseClient(clientID, msg)` | 踢出（先发消息再断开） |
| `DestroyClient(clientID)` | 直接销毁连接 |
| `IsOnline(clientID)` | 判断 client_id 是否在线 → `(bool, error)` |
| `GetAllClientCount()` | 所有在线连接数 → `(int, error)` |
| `GetAllClientIdList()` | 所有在线 client_id 列表 → `([]string, error)` |

---

## PHP vs Go 方法对照

```
PHP GatewaySDK\Gateway::         Go client.
─────────────────────────────       ────────────────────────────
sendToClient($cid, $msg)            SendToClient(cid, msg)
sendToAll($msg)                     SendToAll(msg)
sendToUid($uid, $msg)               SendToUID(uid, msg)
sendToGroup($g, $msg)               SendToGroup(group, msg)
bindUid($cid, $uid)                 BindUID(cid, uid)
unbindUid($cid, $uid)               UnbindUID(cid, uid)
joinGroup($cid, $g)                 JoinGroup(cid, group)
leaveGroup($cid, $g)                LeaveGroup(cid, group)
ungroup($g)                         Ungroup(group)
setSession($cid, $s)                SetSession(cid, session)
updateSession($cid, $s)             UpdateSession(cid, session)
getSession($cid)                    GetSession(cid)
closeClient($cid, $msg)             CloseClient(cid, msg)
destoryClient($cid)                 DestroyClient(cid)
isOnline($cid)                      IsOnline(cid)
isUidOnline($uid)                   IsUidOnline(uid)
getAllClientCount()                  GetAllClientCount()
getClientCountByGroup($g)           GetClientCountByGroup(g)
getAllClientSessions()               GetAllClientSessions()
getClientSessionsByGroup($g)        GetClientSessionsByGroup(g)
getAllClientIdList()                 GetAllClientIdList()
getClientIdListByGroup($g)          GetClientIdListByGroup(g)
getClientIdByUid($uid)              GetClientIdByUid(uid)
getUidByClientId($cid)              GetUidByClientId(cid)
getUidListByGroup($g)               GetUidListByGroup(g)
getUidCountByGroup($g)              GetUidCountByGroup(g)
getAllUidList()                      GetAllUidList()
getAllUidCount()                     GetAllUidCount()
getAllGroupIdList()                  GetAllGroupIdList()
```

---

## 内部机制

### 连接池

GatewaySDK 内置连接池，每个 Gateway 地址维护一条 TCP 连接，带 50 秒 TTL 自动刷新。

### Gateway 地址发现

通过 Register 获取 Gateway 地址列表，缓存 1 秒。当缓存过期时自动重新查询。

### 并发查询

全量查询方法（`GetAllClientCount`、`GetAllUidList` 等）使用 goroutine 并发查询所有 Gateway，等待全部返回后合并结果。

### 加密通讯

所有与 Gateway 的通讯均通过 AES-256-CBC 加密，密钥由 `secretKey` 经 SHA-256 派生。
