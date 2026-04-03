package main

import (
	"ai-gateway/config"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/mq"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings" // 🌟 增加 strings 处理
	"sync"    // 🌟 增加 sync 处理
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// 1. 加载配置
	config.LoadConfig()
	log.Println("1. 开始加载配置...")
	// 初始化数据库
	model.InitDB()

	// 2. 初始化 LLM 供应商
	pName := config.GetEnv("LLM_PRIMARY_NAME", "PrimaryLLM")
	pURL := config.GetEnv("LLM_PRIMARY_URL", "")
	pKey := config.GetEnv("LLM_PRIMARY_KEY", "")
	pModel := config.GetEnv("LLM_PRIMARY_MODEL", "")

	if pURL == "" || pKey == "" {
		log.Fatalf("❌ 错误: 主模型配置不完整")
	}
	primaryProvider := llm.NewBaseClient(pName, pURL, pKey, pModel)

	bName := config.GetEnv("LLM_BACKUP_NAME", "BackupLLM")
	bURL := config.GetEnv("LLM_BACKUP_URL", "")
	bKey := config.GetEnv("LLM_BACKUP_KEY", "")
	bModel := config.GetEnv("LLM_BACKUP_MODEL", "")

	var providers []llm.Provider
	providers = append(providers, primaryProvider)
	if bURL != "" && bKey != "" {
		providers = append(providers, llm.NewBaseClient(bName, bURL, bKey, bModel))
	}

	// 3. 初始化路由器
	router := llm.NewLLMRouter(providers...)

	// 4. 初始化其他组件
	callbackURL := config.GetEnv("CALLBACK_URL", "http://localhost:8080/callback")
	callbackClient := callback.NewCallbackClient(callbackURL)
	redisCache := cache.NewRedisCache(24 * time.Hour)

	// 🌟 修复：处理多个 Kafka Broker 地址 (逗号分隔)
	brokerStr := config.GetEnv("KAFKA_BROKERS", "localhost:9092")
	brokers := strings.Split(brokerStr, ",")

	topic := config.GetEnv("KAFKA_TOPIC", "ai_task")
	groupID := config.GetEnv("CONSUMER_GROUP", "ai_gateway_group")

	// 5. 启动 Prometheus 指标服务
	go func() {
		log.Println("📊 Metrics server starting on :9091/metrics")
		http.Handle("/metrics", promhttp.Handler())
		// 这里不需要 Fatalf，避免监控挂了导致整个网关挂掉
		if err := http.ListenAndServe(":9091", nil); err != nil {
			log.Printf("⚠️ 指标服务启动失败: %v", err)
		}
	}()

	consumer := mq.NewKafkaConsumer(
		brokers,
		topic,
		groupID,
		router,
		callbackClient,
		redisCache,
		5, // workerCount
		"ai_task_dlq",
		3, // maxRetry
	)

	// 6. 准备 Context 和 WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup // 🌟 关键：用于等待协程结束

	// 7. 启动监控上报 (不需要 wg，因为它随 ctx 退出)
	go consumer.ReportStats(ctx)

	// 8. 启动消费者 (需要 wg)
	wg.Add(1)
	go func() {
		defer wg.Done()
		consumer.Start(ctx)
	}()

	log.Println("✅ AI 网关已启动，多模型降级与监控已就绪")

	// 9. 优雅停机逻辑
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("⚠️ 接收到系统信号 [%v]，准备优雅停机...", sig)

	// 🌟 核心步骤：
	cancel()  // 1. 发送取消信号，通知所有协程停止工作
	wg.Wait() // 2. 阻塞等待，直到 consumer.Start 里的所有任务处理完毕返回

	log.Println("✅ 服务已安全退出")
}

/*
消息到达 Kafka ──▶ 拉取消息(Fetch) ──▶ 解析JSON ──▶ AI处理 ──▶ 回调 ──▶ 提交Offset
                    │                    │          │         │          │
                    │                    │          │         │          └── 成功终点
                    │                    │          │         │              (消息不再消费)
                    │                    │          │         │
                    │                    │          │         └── 失败? ────▶ 不提交Offset
                    │                    │          │                        (Kafka重试)
                    │                    │          │
                    │                    │          └── 失败? ────▶ 不提交Offset
                    │                    │                           (整条重试)
                    │                    │
                    │                    └── 解析失败 ────▶ 提交Offset ⚠️ (毒药消息丢弃)
                    │
                    └── 取令牌(limitChan) 控制并发数(workerCount)
*/

/*
┌─────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ 上游系统 │────▶│    Kafka    │────▶│  AI Gateway │────▶│   LLM API   │
└─────────┘     └─────────────┘     └──────┬──────┘     └──────┬──────┘
│                    │
│              ┌─────┴─────┐
│              │  Redis缓存  │
│              │ (task_id→ │
│              │  ai_result) │
│              └───────────┘
▼
┌─────────────┐
│  处理成功?   │──Yes──▶ 提交Offset
└──────┬──────┘
│ No
▼
┌─────────────┐
│  失败类型?   │
└──────┬──────┘
│
┌──────────────────────┼──────────────────────┐
│                      │                      │
▼                      ▼                      ▼
┌─────────┐           ┌─────────┐           ┌─────────┐
│ 解析失败 │           │ AI失败   │           │回调失败  │
│(毒药消息)│           │(可重试)  │           │(可重试)  │
└────┬────┘           └────┬────┘           └────┬────┘
│                     │                     │
▼                     ▼                     ▼
┌─────────┐           ┌─────────┐           ┌─────────┐
│  告警   │           │ 重试队列 │           │ 重试队列 │
│  丢弃   │           │ (指数退避)│           │ (不重新调AI)│
│ 入DLQ   │           │         │           │         │
└─────────┘           └─────────┘           └─────────┘
毒药消息直接丢弃	入死信队列(DLQ) + 告警	可追踪、可人工处理
AI重复调用	Redis缓存结果(task_id幂等)	省钱、避免重复计算
回调失败重走全流程	拆分阶段，回调独立重试	提高效率
无监控指标	接入 Prometheus	可观测
硬编码超时	配置化	灵活调整
*/
