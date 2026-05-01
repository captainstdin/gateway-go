# GatewayWorker-Go

高性能、可扩展的实时通讯框架，基于 Gateway + Worker 分离架构。

## 文档

- [**架构原理**](architecture.md) — 系统设计、组件详解、通讯协议、安全机制
- [**使用指南**](usage.md) — 快速开始、配置参数、业务开发、API 参考、部署方式

## 特性

- 🔌 Gateway/Worker 分离，支持热更新业务逻辑
- 🌐 同时支持 WebSocket 和 TCP 客户端，URI 风格地址配置（`ws://`、`tcp://`）
- 🔒 内部通讯 AES-256-CBC 加密
- ⚖️ 自动负载均衡（最少连接数 / 随机）
- 📡 GatewayClient SDK，任意进程可推送消息
- 🔄 自动断线重连
- 💓 心跳检测
- 🛡️ 多 Register 高可用，防止单点故障
- 📊 Dashboard 实时监控面板（Gateway 在线数 / Worker 处理次数）
