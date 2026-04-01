package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ai-gateway/config"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/mq"
)

func main() {
	// 1. 加载配置
	config.LoadConfig()
	brokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	topic := os.Getenv("KAFKA_TOPIC")
	groupID := os.Getenv("CONSUMER_GROUP")
	callbackURL := os.Getenv("CALLBACK_URL")
	
	dlqTopic := os.Getenv("KAFKA_DLQ_TOPIC")
	if dlqTopic == "" {
		dlqTopic = "ai_task_topic_dlq"
	}
	
	maxRetry := 3
	if v := os.Getenv("MAX_RETRY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxRetry = n
		}
	}

	// 【修复 Bug 1】使用 log.Fatalf，如果配置不对直接终止程序
	if topic == "" || groupID == "" || len(brokers) == 0 || (len(brokers) == 1 && brokers[0] == "") || callbackURL == "" {
		//这个函数Fatalf打印完日志立马退出
		log.Fatalf("❌ 致命错误: Kafka 配置信息不完整，请检查 .env 文件")
	}
	
	// 2. 初始化依赖组件
	log.Println("🚀 正在初始化 AI 异步网关...")
	client := llm.NewLLMClient()
	callbackClient := callback.NewCallbackClient(callbackURL)
	// 读取并发配置
	//控制协程数不然容易炸内存
	workerCount := 1
	workerStr := os.Getenv("WORKER_COUNT")
	if workerStr == ""{
		workerCount = 1
	}else{
		var err error
		workerCount,err = strconv.Atoi(workerStr)
		if err != nil {
			log.Fatalf("❌ WORKER_COUNT 配置错误: %q 不是有效数字", workerStr)
		}
		if workerCount <= 0 || workerCount > 100 {
			log.Fatalf("❌ WORKER_COUNT 超出范围: %d (应在 1-100 之间)", workerCount)
		}
	}

	redisCache := cache.NewRedisCache(24*time.Hour)
	consumer := mq.NewKafkaConsumer(
		brokers, 
		topic, 
		groupID, 
		client, 
		callbackClient,
		redisCache,
		workerCount,
		dlqTopic,    // ← 新增
		maxRetry,    // ← 新增
	)

	
	// 3. 创建带取消功能的 Context
	ctx, cancel := context.WithCancel(context.Background())

	// 【修复 Bug 2】引入 WaitGroup，用于等待后台任务真正结束
	var wg sync.WaitGroup

	// 4. 启动消费者 (放入后台 Goroutine)
	wg.Add(1) // 登记一个员工
	go func() {
		defer wg.Done() // 员工干完活后，打卡下班
		// 假设你的 consumer.Start 是一个阻塞的死循环，直到 ctx 被 cancel 才会 return
		//并不会卡在这里，他开了一个协程Goroutine
		//主协程（main 函数所在的线程）在启动这个新协程后，不会等待 consumer.Start(ctx) 执行完，而是会立即向下执行后面的代码。
		consumer.Start(ctx)
	}()

	// 5. 监听系统退出信号 (优雅停机)
	sigChan := make(chan os.Signal, 1)
	// 监听 Ctrl+C (Interrupt) 和 Docker/K8s 的停止信号 (SIGTERM)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 阻塞在这里，直到收到信号
	sig := <-sigChan
	log.Printf("⚠️ 接收到系统信号: %v，准备优雅停机...", sig)

	// 6. 触发取消信号，通知 consumer 停止拉取新消息，并处理完手头的老消息
	cancel()

	// 7. 老板在门口等员工下班 (阻塞等待 wg 归零)
	log.Println("⏳ 正在等待正在执行的 AI 任务完成...")
	wg.Wait()

	log.Println("✅ 服务已安全退出，所有状态已保存。")
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