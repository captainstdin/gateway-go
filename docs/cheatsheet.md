# 速查手册

> 30 秒理解整个系统。详细内容点击链接跳转。

---

## 系统一句话

**Gateway 管连接，Worker 跑业务，Register 做发现，GatewaySDK 做外推。**

```
客户端 ←WebSocket/TCP→ Gateway ←AES加密→ Worker（你的业务代码）
                          ↑                    ↑
                       Register ←──────────────┘
                          ↑
                      GatewaySDK（HTTP服务/定时任务等外部进程）
```

---

## 你想做什么？

| 我想... | 看这里 |
|---|---|
| 跑起来试试 | [usage.md](usage.md) → 快速开始 |
| 写业务逻辑 | [code-usage.md](code-usage.md) → 修改 `cmd/worker/main.go` |
| 从 HTTP 服务推送消息 | [gateway-sdk.md](gateway-sdk.md) → `gateway_sdk.New()` |
| 了解架构原理 | [architecture.md](architecture.md) |
| 修改框架内核 | ⚠️ 先读 [internals.md](internals.md)（锁、连接池、死锁陷阱） |
| 知道哪些坑已经踩过 | [optimize2ai.md](optimize2ai.md)（20 个已修复问题） |
| 从 PHP 迁移 | [php-vs-go.md](php-vs-go.md) |
| 避免常见错误 | [best-practices.md](best-practices.md) |

---

## 核心文件速查

```
写业务:     cmd/worker/main.go          ← 改这个文件
内部API:    pkg/gateway_api/            ← Worker 回调中用
外部SDK:    pkg/gateway_sdk/            ← 外部进程推送用
协议常量:   pkg/protocol/               ← 命令字、包限制
网关核心:   pkg/gateway/                ← 连接管理（⚠️ 改前读 internals.md）
```

---

## 三条铁律

1. **回调里不要阻塞** → 耗时操作用 `go func(){}()` 异步
2. **Session 要小** → 每条消息都带着传输，放大量数据会拖垮性能
3. **改锁相关代码前读 internals.md 第三章** → 已经修过 3 次"持锁做网络 I/O"的 bug

---

## 启动命令

```bash
# 一键编译
bash cmd/build_all.sh

# 一键启动（register + gateway + worker）
bash cmd/start.sh

# 测试连接
# 浏览器: new WebSocket('ws://127.0.0.1:7272')
```
