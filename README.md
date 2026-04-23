# 🚀 Enterprise AI Gateway (高可用大模型网关)

基于 Golang 开发的企业级大模型调度与分发网关，致力于解决多租户高并发场景下的限流防刷、超长文本超时、以及昂贵算力成本控制等核心痛点。
## 🏗️ 系统架构 / Architecture
![高可用大模型网关架构图](./assets/architecture.png)

## ✨ 核心特性 / Core Features

- **🏎️ 双核调度架构 (Dual-Core Routing)**
  - **快车道 (Sync API)**：处理低延迟的实时对话请求，完美支持 SSE 流式打字机输出。
  - **慢车道 (Async Worker)**：基于 Kafka 的重负载消息队列，对预估 Token 极高的海量文本（如 10万字财报）进行物理隔离削峰。

- **🛡️ 毫秒级强一致性流控 (Atomic Rate Limiting)**
  - 基于 Redis + Lua 脚本，实现了原子级的 RPM（请求频次）与 TPM（Token 额度）双维度令牌桶限流。
  - 结合 `singleflight` 与空值缓存 (Negative Caching) 机制，成功抵御缓存穿透与缓存击穿。

- **🧠 长文本 Map-Reduce 引擎**
  - 集成 `tiktoken` 离线分词，利用带重叠区 (Overlap) 的滑动窗口算法切分超长文本。
  - **断点续传设计**：子任务结果秒级持久化至 Redis，即便网络闪断或发生宕机，也可免除 90% 以上的重试 Token 成本。

- **🚑 高可用容错 (High Availability)**
  - `gobreaker` 状态机实现多模型供应商（如 DeepSeek、Qwen 等）的动态故障转移与降级。
  - 消费者采用**手动提交 Offset + 死信队列 (DLQ)** 机制，确保任何极端情况下计费流水与 AI 生成结果 100% 不丢失。

## 🛠️ 技术栈
Golang 1.22 | Gin | Redis (Lua) | Kafka | MySQL | GORM | Prometheus | Docker

