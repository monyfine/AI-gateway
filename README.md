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
graph TD
    %% 定义外部系统
    Client([客户端 / 业务前端])
    MainSystem([主业务系统])
    LLM_Providers((多模型供应商 DeepSeek/Qwen 等))

    %% AI Gateway 核心模块
    subgraph AI Gateway
        API[API Handler (Gin)]
        Router[LLM Router (熔断与重试策略)]
        Worker[Async Worker (长文本滑动窗口)]
        DLQ_Compensator[Retry DLQ Handler (死信补偿)]
    end

    %% 存储层与中间件
    subgraph Middleware & Storage
        Redis[(Redis)]
        Kafka_Queue{Kafka: Fast/Slow Topic}
        Kafka_DLQ{Kafka: Dead Letter Queue}
        MySQL[(MySQL DB)]
    end

    %% 1. 快车道 (Sync/Stream) 数据流
    Client -- 1. 同步/流式对话请求 --> API
    API -- 2. 限流校验 & 查询缓存 --> Redis
    Redis -. 3a. 命中缓存直接返回 .-> API
    
    API -- 3b. 未命中交由路由 --> Router
    Router -- 4. 熔断判定 & API调用 --> LLM_Providers
    LLM_Providers -- 5. 结果/流式推送 (SSE) --> Router
    Router -- 6. 返回数据给控制器 --> API
    API -- 7. 返回结果给前端 --> Client
    API -. 8a. 异步计费/审计日志 .-> MySQL
    API -. 8b. 异步更新缓存&扣费 .-> Redis

    %% 2. 慢车道 (Async Worker) 数据流
    Client -- 提交超长文本解析任务 --> API
    API -- 发送长任务消息 --> Kafka_Queue
    Kafka_Queue -- 消费任务 --> Worker
    Worker -- 调用路由请求大模型 --> Router
    Worker -. 分片结果断点续传 .-> Redis
    Worker -- 执行完毕回调业务端 --> MainSystem

    %% 3. 容错补偿数据流
    Worker -. 极端失败转入死信 .-> Kafka_DLQ
    Kafka_DLQ -- 拉取死信消息 --> DLQ_Compensator
    DLQ_Compensator -- 重置 Header 投递回原队列 --> Kafka_Queue

    %% 样式调整
    classDef gateway fill:#e1f5fe,stroke:#01579b,stroke-width:2px;
    classDef storage fill:#fff3e0,stroke:#e65100,stroke-width:2px;
    class API,Router,Worker,DLQ_Compensator gateway;
    class Redis,Kafka_Queue,Kafka_DLQ,MySQL storage;
