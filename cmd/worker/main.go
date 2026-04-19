package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"ai-gateway/config"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/mq"
	"ai-gateway/internal/task"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	log.Println("🚀 正在启动 AI Gateway Worker 服务 (双核流控架构)...")
	config.LoadConfig()
	model.InitDB()
	task.StartLogCleaner(30) // 保留 30 天日志

	// 初始化 LLM
	primaryProvider := llm.NewBaseClient(
		config.GetEnv("LLM_PRIMARY_NAME", "PrimaryLLM"),
		config.GetEnv("LLM_PRIMARY_URL", ""),
		config.GetEnv("LLM_PRIMARY_KEY", ""),
		config.GetEnv("LLM_PRIMARY_MODEL", ""),
	)
	fallbackProvider := llm.NewBaseClient(
		config.GetEnv("LLM_BACKUP_NAME", "FallbackLLM"),
		config.GetEnv("LLM_BACKUP_URL", ""),
		config.GetEnv("LLM_BACKUP_KEY", ""),
		config.GetEnv("LLM_BACKUP_MODEL", ""),
	)
	llmRouter := llm.NewLLMRouter(primaryProvider, fallbackProvider)

	// 初始化组件
	callbackURL := config.GetEnv("CALLBACK_URL", "http://localhost:8080/api/v1/articles/callback")
	callbackClient := callback.NewCallbackClient(callbackURL)
	redisCache := cache.NewRedisCache(24 * time.Hour)

	// 获取 Kafka 基础配置
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	dlqTopic := config.GetEnv("KAFKA_DLQ_TOPIC", "ai_task_topic_dlq")
	retryTopic := config.GetEnv("KAFKA_RETRY_TOPIC", "ai_task_topic_retry")
	groupID := config.GetEnv("CONSUMER_GROUP", "ai_gateway_group")

	// 启动 Prometheus 指标服务
	go func() {
		log.Println("📊 Metrics server starting on :9091/metrics")
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":9091", nil); err != nil {
			log.Printf("⚠️ 指标服务启动失败: %v", err)
		}
	}()

	// 🌟 核心修改：给 GroupID 增加后缀
	fastConsumer := mq.NewKafkaConsumer(
		brokers, "ai_task_fast", groupID+"_fast", // 独立组 ID
		llmRouter, callbackClient, redisCache, 10, dlqTopic, retryTopic, 3,
	)

	heavyConsumer := mq.NewKafkaConsumer(
		brokers, "ai_task_heavy_v3", groupID+"_heavy", // 独立组 ID
		llmRouter, callbackClient, redisCache, 2, dlqTopic, retryTopic, 3,
	)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// 分别启动状态汇报
	workerMode := config.GetEnv("WORKER_MODE", "all") // 读取环境变量

	if workerMode == "all" || workerMode == "fast" {
		go fastConsumer.ReportStats(ctx)
		go fastConsumer.Start(ctx)
	}

	if workerMode == "all" || workerMode == "heavy" {
		go heavyConsumer.ReportStats(ctx) 
		go heavyConsumer.Start(ctx)
	}
	// 🌟 新增：启动独立的重试调度中心
	if workerMode == "all" || workerMode == "retry" {
		// 参数：broker列表, 监听的重试Topic, 最终的死信Topic, 最大允许重试次数(如 3 次)
		go mq.StartRetryDispatcher(brokers, retryTopic, dlqTopic, 3)
	}

	// ==========================================
	// 优雅停机逻辑 (完美兼容双消费者)
	// ==========================================
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	sig := <-sigChan
	log.Printf("⚠️ 接收到系统信号 [%v]，准备优雅停机...", sig)

	// 通知所有消费者停止拉取新消息
	cancel()

	// 开启 30 秒强制退出兜底
	go func() {
		time.Sleep(30 * time.Second)
		log.Println("😱 优雅停机超时 (30s)，强制退出！")
		os.Exit(1)
	}()

	// 等待两个消费者把手头正在处理的请求都跑完
	wg.Wait()
	log.Println("✅ Worker 服务已安全退出")
}

/*
OUIVPWEWIEPPL6HM
 */
