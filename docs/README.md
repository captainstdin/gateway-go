# GatewayWorker-Go

高性能、可扩展的实时通讯框架，基于 Gateway + Worker 分离架构。

## 文档

- [**⚡ 速查手册**](cheatsheet.md) — 30 秒理解整个系统，不知道看啥先看这个
- [**架构原理**](architecture.md) — 系统设计、组件详解、通讯协议、安全机制
- [**内部设计备忘**](internals.md) — 🔑 关键设计决策、锁模式、踩坑记录（修改代码前必读）
- [**使用指南**](usage.md) — 快速开始、配置参数、业务开发、API 参考、部署方式
- [**GatewaySDK**](gateway-sdk.md) — 外部进程推送 SDK，完整 API 参考与用法示例
- [**代码示例**](code-usage.md) — 完整的代码用法和示例
- [**开发注意事项**](best-practices.md) — 包大小限制、Session 使用、回调安全、性能规划
- [**PHP vs Go 对比**](php-vs-go.md) — 架构原理、用法、性能、安全、部署的完整对比
- [**AI 优化记录**](optimize2ai.md) — 两轮代码审查修复的 13 处核心问题（带 diff）

## 特性

- 🔌 Gateway/Worker 分离，支持热更新业务逻辑
- 🌐 同时支持 WebSocket 和 TCP 客户端，URI 风格地址配置（`ws://`、`tcp://`、`frame://`、`text://`）
- 🔒 内部通讯 AES-256-CBC 加密
- ⚖️ 自动负载均衡（最少连接数 / 随机）
- 📡 GatewaySDK，任意进程可推送消息
- 🔄 自动断线重连
- 💓 心跳检测
- 🛡️ 多 Register 高可用，防止单点故障
- 📊 Dashboard 实时监控面板（Gateway 在线数 / Worker 处理次数）
- 🔗 完整 API 对标 PHP 版（37 个方法全覆盖）
