package mq

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm" // 确保你的 go.mod 里模块名是 ai-gateway
	"ai-gateway/pkg/metrics"

	"github.com/segmentio/kafka-go"
)

// KafkaConsumer 消费者结构体
type KafkaConsumer struct {
	Reader         *kafka.Reader
	llmRouter      *llm.LLMRouter
	callbackClient *callback.CallbackClient
	limitChan      chan struct{}
	workerCount    int
	redisCache     *cache.RedisCache

	dlqProducer *DLQProducer
	maxRetry    int
}

// TaskMessage 任务消息结构体
// 🚨 注意：字段首字母必须大写！使用 json tag 映射实际的 key
type TaskMessage struct {
	TaskID  string `json:"task_id"` // 建议用 string，兼容性更好
	Content string `json:"content"`
}

// NewKafkaConsumer 构造函数 (参数改为小驼峰命名)
func NewKafkaConsumer(
	brokers []string,
	topic string,
	groupID string,
	router *llm.LLMRouter,
	cbClient *callback.CallbackClient,
	redisCache *cache.RedisCache,
	workerCount int,
	dlqTopic string,
	maxRetry int,
) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID,
		MinBytes: 10e3, // 10KB
		MaxBytes: 10e6, // 10MB
	})

	return &KafkaConsumer{
		Reader:         reader,
		llmRouter:      router,
		callbackClient: cbClient, // 注入进来
		limitChan:      make(chan struct{}, workerCount),
		workerCount:    workerCount,
		redisCache:     redisCache,
		dlqProducer:    NewDLQProducer(brokers, dlqTopic), // ← 用传进来的 dlqTopic
		maxRetry:       maxRetry,                          // ← 用传进来的 maxRetry
	}
}

// Start 启动消费者阻塞循环
func (c *KafkaConsumer) Start(ctx context.Context) {
	log.Println("🚀 Kafka 消费者已启动，正在监听消息...")
	defer c.Close()

	var wg sync.WaitGroup // 跟踪所有正在处理的任务

	// 【修复 Bug 2】无论 Start 方法如何退出，都必须等待所有后台任务完成！
	defer func() {
		log.Println("⏳ 正在等待所有后台 AI 任务处理完毕...")
		wg.Wait()
		log.Println("✅ 所有后台任务已圆满结束")
	}()

	for {
		// 【关键】先拿令牌，控制并发
		select {
		case c.limitChan <- struct{}{}:
			// 拿到令牌，可以继续
		case <-ctx.Done():
			// 收到停机信号，不再处理新消息
			log.Println("🛑 停止拉取新消息，等待处理中的任务...")
			return
		}

		// 1. 拉取消息 (Fetch 不会提交 Offset)
		// 这里的 ctx 是 main 函数传来的，如果 main 调用了 cancel()，FetchMessage 会立刻解除阻塞并返回 context.Canceled
		msg, err := c.Reader.FetchMessage(ctx)
		if err != nil {
			<-c.limitChan // 释放令牌
			if errors.Is(err, context.Canceled) {
				log.Println("🛑 收到退出信号")
				wg.Wait()
				return
			}
			log.Printf("❌ 拉取消息失败: %v\n", err)
			time.Sleep(2 * time.Second) // 🌟 失败后歇 2 秒再试，给 Kafka 喘息的机会
			continue
		}

		log.Printf("📥 收到消息: Topic=%s, Partition=%d, Offset=%d\n",
			msg.Topic, msg.Partition, msg.Offset)

		// 异步处理（在 Goroutine 里）
		wg.Add(1)
		// 传入 ctx 和 wg
		go func(m kafka.Message) {
			defer wg.Done()
			defer func() { <-c.limitChan }()
			c.processMessage(ctx, m) // 传递 ctx
		}(msg)
	}
}

func (c *KafkaConsumer) processMessage(ctx context.Context, msg kafka.Message) {
	var task TaskMessage
	if err := json.Unmarshal(msg.Value, &task); err != nil {
		c.sendToDLQAndCommit(msg, "JSON解析失败", 0)
		return
	}

	// 1. 检查 Redis 里的累计重试次数 (分布式持久化)
	currentRetry, _ := c.redisCache.GetRetryCount(task.TaskID)
	if currentRetry >= c.maxRetry {
		log.Printf("☠️ 任务 [%s] 累计重试已达上限 (%d), 转入DLQ", task.TaskID, currentRetry)
		c.sendToDLQAndCommit(msg, "分布式重试超限", currentRetry)
		c.redisCache.ClearRetryCount(task.TaskID) // 进死信后清理
		return
	}

	// 2. 查缓存 (商业思维：命中缓存 = 0 Token 消耗)
	if cached, ok := c.redisCache.GetAIResult(task.TaskID); ok {
		log.Printf("💰 缓存命中 [%s]，节省了 Token 成本", task.TaskID)
		if err := c.callbackClient.SendResult(ctx, task.TaskID, cached); err == nil {
			c.commit(msg)
			c.redisCache.ClearRetryCount(task.TaskID)
			return
		}
	}

	// 3. 执行业务逻辑 (调用 AI)
	// 🌟 修改点：接收 aiResult 和 usage
	aiResult, usage, err := c.llmRouter.InvokeWithFallback(ctx, task.Content)

	// 🌟 核心修改点：构造数据库日志对象
	logEntry := model.RequestLog{
		TaskID:           task.TaskID,
		Prompt:           task.Content,
		Response:         aiResult,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		Status:           "success",
	}
	if err != nil {
		logEntry.Status = "fail"
		logEntry.ErrorMsg = err.Error()
		model.DB.Create(&logEntry)

		// 失败处理 (保持不变)
		metrics.TasksTotal.WithLabelValues("fail").Inc()
		newCount, _ := c.redisCache.IncrRetryCount(task.TaskID)
		log.Printf("⚠️ 任务 [%s] AI调用失败 (当前累计重试: %d/%d): %v", task.TaskID, newCount, c.maxRetry, err)
		return
	}

	//成功了，把完整的记录存入 MySQL
	model.DB.Create(&logEntry)

	// 🌟 4. 商业逻辑：记录 Token 消耗 (计费)
	// 注意：即使后续回调失败，Token 已经花了，必须记录
	if err := c.redisCache.RecordTokenUsage(task.TaskID, usage); err != nil {
		log.Printf("⚠️ Token 计费记录失败 [%s]: %v", task.TaskID, err)
	}
	// 更新全局大盘指标
	c.redisCache.IncrGlobalTokenStats(usage)

	// 5. 成功处理
	metrics.TasksTotal.WithLabelValues("success").Inc()

	// 写结果缓存
	_ = c.redisCache.SetAIResult(task.TaskID, aiResult)

	// 回调主系统
	if err := c.callbackClient.SendResult(ctx, task.TaskID, aiResult); err != nil {
		log.Printf("⚠️ 回调失败 [%s]，等待 Kafka 重试 (下次将走缓存)", task.TaskID)
		// 不提交 Offset，触发 Kafka 重试
		return
	}

	log.Printf("✅ 任务 [%s] 完成，消耗 Token: %d", task.TaskID, usage.TotalTokens)
	c.redisCache.ClearRetryCount(task.TaskID)
	c.commit(msg)
}

// Close 优雅关闭 (首字母大写，供外部调用)
func (c *KafkaConsumer) Close() error {
	if c.dlqProducer != nil {
		c.dlqProducer.Close()
	}
	return c.Reader.Close()
}

// commit 内部辅助方法，自带 5 秒超时控制，防止 Kafka 宕机导致卡死
func (c *KafkaConsumer) commit(msg kafka.Message) {
	// 创建一个独立的 5 秒超时 Context
	commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel() // 记得释放

	if err := c.Reader.CommitMessages(commitCtx, msg); err != nil {
		log.Printf("⚠️ 提交 Offset 失败[Offset: %d]: %v\n", msg.Offset, err)
	} else {
		log.Printf("🔖 成功提交 Offset: %d\n", msg.Offset)
	}
}

func (c *KafkaConsumer) sendToDLQAndCommit(msg kafka.Message, reason string, retryCount int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := c.dlqProducer.SendToDLQ(ctx, msg, reason, retryCount); err != nil {
		log.Printf("❌ DLQ 投递失败，保留原消息 [Offset: %d]", msg.Offset)
		return
	}
	c.commit(msg)
}

func (c *KafkaConsumer) ReportStats(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := c.Reader.Stats()
			// Lag 是积压量的关键指标
			metrics.KafkaLag.WithLabelValues(stats.Topic, "all").Set(float64(stats.Lag))
		}
	}
}
