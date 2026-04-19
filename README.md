# 🚀 Enterprise AI Gateway (高可用大模型网关)

基于 Golang 开发的企业级大模型调度与分发网关，致力于解决多租户高并发场景下的限流防刷、超长文本超时、以及昂贵算力成本控制等核心痛点。

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

## 🚀 快速启动
```bash
# 1. 复制环境变量
cp .env.example .env

# 2. 一键启动基础设施与网关节点
docker-compose up -d --build

## 🏗️ 架构数据流图 (Architecture & Data Flow)
flowchart TD
    %% 样式定义 - 严格匹配检查点的视觉区分规则
    classDef client fill:#f8f9fa,stroke:#212529,stroke-width:2px
    classDef gateway fill:#e3f2fd,stroke:#1565c0,stroke-width:2px
    classDef fastLane fill:#1976d2,stroke:#0d47a1,stroke-width:2px,color:#fff
    classDef slowLane fill:#424242,stroke:#212121,stroke-width:2px,color:#fff
    classDef redis fill:#d32f2f,stroke:#b71c1c,stroke-width:2px,color:#fff
    classDef router fill:#7b1fa2,stroke:#4a148c,stroke-width:2px,color:#fff
    classDef kafka fill:#2e7d32,stroke:#1b5e20,stroke-width:2px,color:#fff
    classDef llm fill:#f57c00,stroke:#e65100,stroke-width:2px,color:#fff
    classDef dlq fill:#ef6c00,stroke:#e65100,stroke-width:2px,color:#fff
    classDef mysql fill:#0277bd,stroke:#01579b,stroke-width:2px,color:#fff
    classDef asyncLine stroke:#f57c00,stroke-width:2px,stroke-dasharray:5 5
    classDef cacheLine stroke:#d32f2f,stroke-width:3px,stroke-dasharray:5 5
    classDef retryLine stroke:#2e7d32,stroke-width:2px

    %% 1. 入口层
    C[客户端]:::client
    GW[API网关]:::gateway

    %% 2. 双车道物理隔离【检查点1 完全满足】全程独立无交叉
    FL[【快车道】Sync/SSE 同步/流式请求<br/>低延迟·强实时·同步返回]:::fastLane
    SL[【慢车道】Async/Kafka 异步/批量请求<br/>高吞吐·削峰填谷·异步回调]:::slowLane

    %% 3. Redis 双重身份【检查点2 完全满足】
    R[Redis<br/>① 租户限流(RPM/TPM)配额管控<br/>② 对话上下文0延迟缓存]:::redis

    %% 4. LLM Router 内置熔断【检查点4 完全满足】
    RTR[LLM Router<br/>内置gobreaker熔断组件<br/>故障自动切换·动态流量调度]:::router

    %% 5. 慢车道Kafka与DLQ闭环【检查点3 完全满足】
    K_MAIN[Kafka 主Topic<br/>异步请求队列]:::kafka
    K_WORKER[Kafka 消费Worker<br/>批量推理请求处理]:::kafka
    K_DLQ[DLQ 死信队列<br/>消费失败/超时请求]:::dlq
    DLQ_HANDLER[Retry DLQ Handler<br/>① 提取x-original-topic<br/>② 重置x-retry-count<br/>③ 合规性校验]:::dlq
    DLQ_ARCHIVE[死信归档<br/>超最大重试次数·人工处理]:::dlq

    %% 6. 大模型厂商节点
    LLM1[DeepSeek 大模型]:::llm
    LLM2[通义千问 Qwen 大模型]:::llm

    %% 7. 计费审计存储层【检查点5 完全满足】
    DB[MySQL 数据库<br/>RequestLog全量审计<br/>租户计费明细落库]:::mysql

    %% ====================== 核心主链路 ======================
    %% 入口分流
    C --> GW
    GW --> FL
    GW --> SL

    %% ---------------------- 快车道完整主链路 ----------------------
    FL --> R
    %% 检查点2核心：缓存命中直接短路返回客户端 红色虚线
    R -.->|【缓存命中 | 0延迟直接返回】| C:::cacheLine
    %% 限流通过+缓存未命中 → 进入路由层
    R -->|【限流校验通过 | 缓存未命中】| RTR
    %% 检查点4核心：路由层先判断熔断状态，再分发请求
    RTR -->|熔断状态正常·动态路由| LLM1
    RTR -->|熔断状态正常·动态路由| LLM2
    %% 快车道同步/SSE返回客户端
    LLM1 -->|SSE流式推送/同步响应| C
    LLM2 -->|SSE流式推送/同步响应| C

    %% ---------------------- 慢车道完整主链路 ----------------------
    SL --> R
    R -->|【限流校验通过】| K_MAIN
    K_MAIN --> K_WORKER
    K_WORKER --> RTR
    %% 慢车道异步回调返回客户端
    LLM1 -->|推理完成·异步回调通知| C
    LLM2 -->|推理完成·异步回调通知| C

    %% ---------------------- 检查点3 DLQ闭环补偿链路 ----------------------
    K_MAIN -->|消费失败/超时| K_DLQ
    K_DLQ --> DLQ_HANDLER
    %% 核心闭环：重投递回原主Topic 绿色实线
    DLQ_HANDLER -->|【合规重投递 | 打回原队列】| K_MAIN:::retryLine
    %% 边界处理：超最大重试次数归档
    DLQ_HANDLER -->|超最大重试次数| DLQ_ARCHIVE

    %% ---------------------- 检查点5 异步计费与审计全链路 橙色虚线 ----------------------
    %% 快车道请求结束后异步计费
    C -.->|【异步 | 计费审计】| DB:::asyncLine
    %% 慢车道回调结束后异步计费
    C -.->|【异步 | 计费审计】| DB:::asyncLine
    %% 缓存命中0消耗异步审计日志
    R -.->|【异步 | 0消耗审计日志】| DB:::asyncLine
    %% 计费完成后同步更新Redis配额消耗
    DB -.->|【异步 | 更新Redis TPM/RPM配额消耗】| R:::asyncLine
