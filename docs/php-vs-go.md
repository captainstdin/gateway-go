# PHP vs Go 版本对比

> GatewayWorker PHP 版 vs gatewayworker-go 的完整对比分析

---

## 目录

- [一、架构原理对比](#一架构原理对比)
- [二、用法对比](#二用法对比)
- [三、性能优势对比](#三性能优势对比)
- [四、安全机制对比](#四安全机制对比)
- [五、部署运维对比](#五部署运维对比)
- [六、API 对照表](#六api-对照表)

---

## 一、架构原理对比

### 1.1 整体架构

两个版本遵循相同的三进程架构：**Register + Gateway + Worker**，但底层实现原理完全不同。

```
┌──────────────────────────────────────────────────┐
│                  架构一致                          │
│   Register ←→ Gateway ←→ Worker                  │
│   (服务发现)    (连接管理)   (业务逻辑)               │
└──────────────────────────────────────────────────┘
```

### 1.2 进程/并发模型

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 运行时 | Workerman（PHP CLI 事件驱动框架） | Go runtime（goroutine 调度） |
| 并发模型 | **单线程事件循环** + 多进程 | **goroutine 协程** + 单进程 |
| 多核利用 | 通过 `fork()` 创建多个 Worker 进程 | goroutine 自动调度到多核 |
| 内存模型 | 每个进程独立内存空间 | 单进程共享内存，`sync.RWMutex` 保护 |
| Gateway 并发 | 多进程，每进程单线程事件循环 | 单进程，每连接一个 goroutine |

**原理区别解析：**

PHP 版依赖 Workerman 框架，使用 `pcntl_fork()` 创建多进程，每个进程是单线程事件循环（epoll/select）。多核利用通过进程数控制：

```php
// PHP: 需要启动多个进程实例
$gateway = new Gateway("websocket://0.0.0.0:7272");
$gateway->count = 4;  // fork 4 个进程
```

Go 版使用 goroutine，每个客户端连接一个 goroutine，Go runtime 自动将 goroutine 调度到多个 OS 线程：

```go
// Go: 单进程天然多核
gateway := gateway.New(&gateway.Config{
    ListenAddrs: []string{"ws://0.0.0.0:7272"},
})
// 无需配置进程数，goroutine 自动利用所有 CPU 核心
```

### 1.3 I/O 模型

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 事件驱动 | Workerman 封装的 `stream_select` / epoll | Go 标准库 netpoll（底层也是 epoll/kqueue） |
| 读写方式 | 非阻塞 I/O + 事件回调 | 阻塞式 goroutine（runtime 透明异步） |
| 编程范式 | 回调风格 | 同步顺序风格（看起来阻塞，实际是协程切换） |

### 1.4 内部通讯加密

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 加密方式 | **无加密**（明文传输），依赖 `$secretKey` 做简单认证 | **AES-256-CBC** 全链路加密 |
| Register 通讯 | 明文 JSON + `\n` | Base64(AES(JSON)) + `\n` |
| Gateway-Worker 通讯 | 二进制协议明文传输 | `[4B长度][AES加密的二进制协议]` |
| 密钥派生 | 直接使用字符串比对 | SHA-256 派生 32 字节 AES 密钥 |

**这是 Go 版最大的安全改进**。PHP 版在内网传输是完全明文的，`secretKey` 仅用于注册时的身份校验。Go 版所有内部组件间的通讯都经过 AES-256-CBC 加密，即使内网被嗅探也无法读取。

### 1.5 数据序列化

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| Session 序列化 | `serialize()` / `unserialize()` (PHP 原生) | JSON `Marshal` / `Unmarshal` |
| 协议扩展数据 | `json_encode` + `serialize` 混用 | 统一 JSON |
| 查询响应 | `serialize` | JSON |

Go 版统一使用 JSON，跨语言兼容性更好。PHP 版的 `serialize` 格式是 PHP 专有的。

---

## 二、用法对比

### 2.1 启动方式

**PHP 版**：

```php
// start_register.php
$register = new Register('text://0.0.0.0:1236');
$register->secretKey = 'your-secret-key';

// start_gateway.php
$gateway = new Gateway("websocket://0.0.0.0:7272");
$gateway->registerAddress = '127.0.0.1:1236';
$gateway->count = 4;
$gateway->secretKey = 'your-secret-key';

// start_worker.php
$worker = new BusinessWorker();
$worker->registerAddress = '127.0.0.1:1236';
$worker->secretKey = 'your-secret-key';

// 启动：php start.php start
```

**Go 版**：

```go
// 每个组件是独立的 main 程序
// cmd/register/main.go
reg := register.New("0.0.0.0:1236", "your-secret-key")
reg.Run()

// cmd/gateway/main.go
gw := gateway.New(&gateway.Config{
    ListenAddrs:   []string{"ws://0.0.0.0:7272"},
    LanIP:         "0.0.0.0",         // 本地监听IP
    RegisterLanIP: "127.0.0.1",       // 上报给Register的通讯IP
    RegisterAddr:  []string{"127.0.0.1:1236"},
    SecretKey:     "your-secret-key",
})
gw.Run()

// cmd/worker/main.go
bw := worker.New("MyWorker", 0, []string{"127.0.0.1:1236"}, "your-secret-key")
bw.OnMessage = func(clientID string, msg []byte) { ... }
bw.Run()
```

### 2.2 业务回调

**PHP 版**：

```php
class Events {
    public static function onConnect($client_id) { }
    public static function onMessage($client_id, $message) {
        Gateway::sendToClient($client_id, "hello");
        Gateway::sendToAll("broadcast");
    }
    public static function onClose($client_id) { }
}
```

**Go 版**：

```go
bw.OnConnect = func(clientID string) { }
bw.OnMessage = func(clientID string, message []byte) {
    gateway_api.SendToClient(clientID, []byte("hello"))
    gateway_api.SendToAll([]byte("broadcast"), nil, nil)
}
bw.OnClose = func(clientID string) { }
```

**区别**：
- PHP 使用静态类方法作为事件回调（通过 `$worker->eventHandler` 配置类名）
- Go 使用函数类型字段，更灵活（可以是闭包、方法引用等）
- Go 的 `message` 是 `[]byte` 而非 `string`，避免不必要的内存拷贝

### 2.3 Gateway API 调用

**PHP 版**：

```php
// 全局静态方法，任何地方都能调用
Gateway::sendToClient($client_id, $msg);
Gateway::bindUid($client_id, $uid);
Gateway::sendToGroup('room1', $msg);
$session = Gateway::getSession($client_id);
$online = Gateway::isOnline($client_id);
$count = Gateway::getAllClientCount();
Gateway::sendToAll($msg, null, [$exclude_id]);
Gateway::sendToGroup('room1', $msg, [$exclude_id]);
```

**Go 版**：

```go
// 包级函数，需要先 SetBusinessWorker
gateway_api.SetBusinessWorker(bw)

gateway_api.SendToClient(clientID, msg)
gateway_api.BindUID(clientID, uid)
gateway_api.SendToGroup([]string{"room1"}, msg, nil)
session, err := gateway_api.GetSession(clientID)
online := gateway_api.IsOnline(clientID)
count := gateway_api.GetAllClientCount()
gateway_api.SendToAll(msg, nil, []string{excludeID})
gateway_api.SendToGroup([]string{"room1"}, msg, []string{excludeID})
```

**关键区别**：

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 调用方式 | `Gateway::method()` 静态方法 | `gateway_api.Function()` 包级函数 |
| Group 参数 | 支持字符串或数组 | 统一 `[]string` 强类型 |
| UID 参数 | 支持 `int\|string\|array` | 统一 `[]string` |
| 错误处理 | 抛异常或 echo | 返回 `error`（Go 惯例） |
| 当前客户端 | `Gateway::sendToCurrentClient()` 自动获取上下文 | `SendToCurrentClient(clientID, msg)` 需传 clientID |

### 2.4 外部调用

**PHP 版**（在 HTTP 控制器中推送消息）：

```php
// 直接用 Gateway 类，自动连接 Register 获取地址
Gateway::$registerAddress = '127.0.0.1:1236';
Gateway::$secretKey = 'your-secret-key';
Gateway::sendToUid($uid, $message);
```

**Go 版**：

```go
// 使用 gateway_sdk 包
client := gateway_sdk.New([]string{"127.0.0.1:1236"}, "your-secret-key")
defer client.Close()
client.SendToUID("user123", []byte(message))
```

**区别**：PHP 版的 `Gateway` 类同时支持 Worker 内部和外部调用（自动检测环境）。Go 版将两个场景拆分为 `gateway_api`（Worker 内部）和 `gateway_sdk`（外部调用）两个独立包，职责更清晰。

---

## 三、性能优势对比

### 3.1 并发连接数

| 维度 | PHP 版 | Go 版 | 优势 |
|---|---|---|---|
| 单进程连接上限 | 受 `ulimit` 和事件循环限制，通常 1-5 万 | goroutine 极轻量，单进程可达 **10-100 万** | **Go 10-20x** |
| 多核利用 | 需 fork 多进程，进程间无法共享内存 | 单进程自动多核，共享内存 | **Go 更高效** |
| 上下文切换 | OS 进程切换，开销大 | goroutine 切换 ~200ns，用户态调度 | **Go 100x** |

### 3.2 内存使用

| 维度 | PHP 版 | Go 版 | 优势 |
|---|---|---|---|
| 每进程基础内存 | ~10-30MB（PHP 解释器 + Workerman） | ~5-10MB（Go 二进制） | **Go 2-3x** |
| 每连接内存 | PHP 数组开销大，~2-5KB/连接 | Go struct 紧凑，~0.5-2KB/连接 | **Go 2-5x** |
| 多进程总内存 | N 进程 × 单进程内存（不共享） | 单进程共享 | **Go N倍** |

### 3.3 查询性能

| 操作 | PHP 版 | Go 版 | 优势 |
|---|---|---|---|
| 查询所有 Gateway | `stream_select` **串行轮询** | goroutine + WaitGroup **并发查询** | **Go 显著更快** |
| 广播消息 | 循环 `fwrite` | 可并发写 | **Go 更快** |
| Session 序列化 | `serialize` (较慢) | JSON (较快，且编译型) | **Go 更快** |

### 3.4 启动与部署

| 维度 | PHP 版 | Go 版 | 优势 |
|---|---|---|---|
| 启动速度 | 秒级（加载 PHP 解释器 + autoload） | 毫秒级（编译好的二进制） | **Go 快** |
| 部署产物 | PHP 源码 + Composer 依赖 + PHP 运行时 | **单个静态二进制** | **Go 极简** |
| 跨平台 | 需要目标机器安装 PHP | `GOOS=linux go build` 交叉编译 | **Go 更方便** |
| 依赖管理 | Composer | Go Modules | 相当 |

---

## 四、安全机制对比

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 内部通讯加密 | ❌ 明文 | ✅ AES-256-CBC 全链路加密 |
| 密钥验证 | 字符串比对 | SHA-256 派生密钥 + 加密验证 |
| Register 通讯 | 明文 JSON | 加密 JSON |
| 认证超时 | 无 | 10 秒认证超时，超时断开 |
| IV 随机性 | N/A | 每次加密使用 `crypto/rand` 随机 IV |

---

## 五、部署运维对比

### 5.1 部署架构

**PHP 版**：

```bash
# 需要 PHP 环境
apt install php-cli php-posix php-pcntl
composer install

# 启动（所有组件在一个 start.php 中）
php start.php start -d   # 后台启动
php start.php status      # 查看状态
php start.php stop        # 停止
php start.php restart     # 重启
```

**Go 版**：

```bash
# 编译成独立二进制
go build -o register ./cmd/register
go build -o gateway  ./cmd/gateway
go build -o worker   ./cmd/worker

# 各组件独立启动
./register &
./gateway &
./worker &

# 或使用 start.sh 统一管理
```

### 5.2 进程管理

| 维度 | PHP 版 | Go 版 |
|---|---|---|
| 进程管理 | Workerman 内置（master/worker 模型） | 自行管理或 systemd/supervisor |
| 平滑重启 | `php start.php reload` | 需自行实现或依赖外部工具 |
| 状态查看 | `php start.php status`（内置表格） | 需通过 Dashboard 或日志 |
| 日志 | Workerman 内置日志 | Go `log` 标准库 |

### 5.3 水平扩展

两个版本都支持多 Gateway + 多 Worker 集群化部署，但实现方式不同：

**PHP 版**：通过 `$gateway->count = 4` 创建多进程，同机器多核利用
**Go 版**：单进程已利用多核，扩展主要是多机器部署

---

## 六、API 对照表

### 完整方法映射

```
PHP Gateway::                    Go gateway_api.
─────────────────────────────    ────────────────────────────
sendToClient($cid, $msg)        SendToClient(cid, msg)
sendToCurrentClient($msg)       SendToCurrentClient(cid, msg)  // 需传 clientID
sendToAll($msg, $cids, $ex)     SendToAll(msg, cids, excludes)
sendToUid($uid, $msg)           SendToUID(uids, msg)           // 统一 []string
sendToGroup($g, $msg, $ex)      SendToGroup(groups, msg, ex)   // 统一 []string
bindUid($cid, $uid)             BindUID(cid, uid)
unbindUid($cid, $uid)           UnbindUID(cid, uid)
joinGroup($cid, $g)             JoinGroup(cid, group)
leaveGroup($cid, $g)            LeaveGroup(cid, group)
ungroup($g)                     Ungroup(group)
setSession($cid, $s)            SetSession(cid, session)
updateSession($cid, $s)         UpdateSession(cid, session)
getSession($cid)                GetSession(cid)                // 返回 (map, error)
closeClient($cid, $msg)         CloseClient(cid, msg)
destoryClient($cid)             DestroyClient(cid)
isOnline($cid)                  IsOnline(cid)                  // 返回 bool
isUidOnline($uid)               IsUidOnline(uid)               // 返回 bool
getAllClientCount()              GetAllClientCount()             // 返回 int
getClientCountByGroup($g)       GetClientCountByGroup(g)
getAllClientSessions($g)         GetAllClientSessions(g)
getClientSessionsByGroup($g)    GetClientSessionsByGroup(g)
getAllClientIdList()             GetAllClientIdList()
getClientIdListByGroup($g)      GetClientIdListByGroup(g)
getClientIdByUid($uid)          GetClientIdByUid(uid)
getClientIdByUids($uids)        GetClientIdByUids(uids)
getUidListByGroup($g)           GetUidListByGroup(g)
getUidCountByGroup($g)          GetUidCountByGroup(g)
getAllUidList()                  GetAllUidList()
getAllUidCount()                 GetAllUidCount()
getUidByClientId($cid)          GetUidByClientId(cid)
getAllGroupIdList()              GetAllGroupIdList()
getAllGroupUidList()             GetAllGroupUidList()
getAllGroupUidCount()            GetAllGroupUidCount()
getAllGroupClientIdList()        GetAllGroupClientIdList()
getAllGroupClientIdCount()       GetAllGroupClientIdCount()
```

---

## 总结

| 选择 PHP 版的理由 | 选择 Go 版的理由 |
|---|---|
| 团队 PHP 技术栈 | 需要更高并发连接数 |
| Workerman 生态成熟 | 需要更低内存占用 |
| 内置进程管理和热重载 | 需要内部通讯加密 |
| 快速原型开发 | 需要单二进制部署 |
| | 需要并发查询性能 |
| | 需要跨平台编译 |
