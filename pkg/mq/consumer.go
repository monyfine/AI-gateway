package mq

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm" // 确保你的 go.mod 里模块名是 ai-gateway

	"github.com/segmentio/kafka-go"
)

// KafkaConsumer 消费者结构体
type KafkaConsumer struct {
	Reader         *kafka.Reader
	llmClient      *llm.LLMClient // 依赖注入 LLM 客户端
	callbackClient *callback.CallbackClient
	limitChan      chan struct{}
	workerCount    int
	redisCache     *cache.RedisCache

	dlqProducer *DLQProducer
	maxRetry    int
	retryTracker map[string]int
	retryMu      sync.RWMutex
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
	client *llm.LLMClient, 
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
		llmClient:      client,
		callbackClient: cbClient, // 注入进来
		limitChan:      make(chan struct{}, workerCount),
		workerCount:    workerCount,
		redisCache:     redisCache,
		dlqProducer:    NewDLQProducer(brokers, dlqTopic),  // ← 用传进来的 dlqTopic
		maxRetry:       maxRetry,                           // ← 用传进来的 maxRetry
		retryTracker:   make(map[string]int),
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
			continue
		}

		log.Printf("📥 收到消息: Topic=%s, Partition=%d, Offset=%d\n",
			msg.Topic, msg.Partition, msg.Offset)

		// 异步处理（在 Goroutine 里）
		wg.Add(1)
		go func(m kafka.Message) {
			defer wg.Done()
			defer func() { <-c.limitChan }() // 【关键】任务完成，释放令牌！

			c.processMessage(m) // 调用提取的处理函数
		}(msg)
	}
}

// processMessage 处理单条消息（提取成独立函数）
func (c *KafkaConsumer) processMessage(msg kafka.Message) {
	// 1. 解析 JSON
	var task TaskMessage
	if err := json.Unmarshal(msg.Value, &task); err != nil {
		log.Printf("☠️ 毒药消息警告! Offset: %d, Error: %v\n",msg.Offset, err)
		c.sendToDLQAndCommit(msg, "JSON解析失败: "+err.Error(), 0)
		c.commit(msg) // 毒药消息直接提交，跳过
		return
	}

	retryCount := c.getRetryCount(task.TaskID)
	if retryCount >= c.maxRetry {
		log.Printf("☠️ 任务 [%s] 重试超限 (%d/%d)，转入DLQ\n", task.TaskID, retryCount, c.maxRetry)
		c.sendToDLQAndCommit(msg, "重试次数超限", retryCount)
		c.clearRetryCount(task.TaskID)
		return
	}

	// 🌟 2. 查缓存
	if cached, ok := c.redisCache.GetAIResult(task.TaskID); ok{
		log.Printf("💰 缓存命中 [%s]", task.TaskID)
		workCtx, workCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer workCancel() // 确保资源释放
		
		if err := c.callbackClient.SendResult(workCtx, task.TaskID, cached); err != nil {
			c.incrementRetryCount(task.TaskID)
			return
		}
		c.clearRetryCount(task.TaskID)
		c.commit(msg)
		return
	}


	// 2. 调 AI
	log.Printf("🧠 开始处理任务 [%s]\n", task.TaskID)

	workCtx, workCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer workCancel() // 确保资源释放

	aiResult, err := c.llmClient.InvokeLLM(workCtx, task.Content)

	if err != nil {
		c.incrementRetryCount(task.TaskID)
		return
	}

	go func ()  {
		if err := c.redisCache.SetAIResult(task.TaskID, aiResult); err != nil {
			log.Printf("⚠️ 缓存写入失败 [%s]: %v", task.TaskID, err)
		} else {
			log.Printf("💾 已缓存结果 [%s]", task.TaskID)
		}
	}()	
	
	// 3. 回调主系统
	log.Printf("📤 任务 [%s] 回调主系统\n", task.TaskID)
	if err := c.callbackClient.SendResult(workCtx, task.TaskID, aiResult); err != nil {
		log.Printf("⚠️ 任务 [%s] 回调失败: %v\n", task.TaskID, err)
		return // 不提交，Kafka 会重试
	}

	log.Printf("✅ 任务 [%s] 全流程完成\n", task.TaskID)

	// 4. 只有 AI + 回调都成功，才提交
	c.clearRetryCount(task.TaskID)
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

func (c *KafkaConsumer) getRetryCount(taskID string) int {
	c.retryMu.RLock()
	defer c.retryMu.RUnlock()
	return c.retryTracker[taskID]
}

func (c *KafkaConsumer) incrementRetryCount(taskID string) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	c.retryTracker[taskID]++
}

func (c *KafkaConsumer) clearRetryCount(taskID string) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	delete(c.retryTracker, taskID)
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