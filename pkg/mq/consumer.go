package mq

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/callback"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/metrics"
	"ai-gateway/pkg/tokenizer"

	"github.com/segmentio/kafka-go"
)

// KafkaConsumer 核心消费者结构体
type KafkaConsumer struct {
	Reader         *kafka.Reader
	llmRouter      *llm.LLMRouter
	callbackClient *callback.CallbackClient
	limitChan      chan struct{}
	workerCount    int
	redisCache     *cache.RedisCache

	dlqProducer   *DLQProducer // 🌟 专用于：永久错误 (人工介入)
	retryProducer *DLQProducer // 🌟 专用于：临时错误 (延迟重试)

	maxRetry int
}

type TaskMessage struct {
	TaskID   string `json:"task_id"`
	AppKeyID uint   `json:"app_key_id"`
	APIKey   string `json:"api_key"`
	RPMLimit int    `json:"rpm_limit"`
	TPMLimit int    `json:"tpm_limit"`
	Content  string `json:"content"`
}

// NewKafkaConsumer 初始化消费者 (注意新增了 retryTopic 参数)
func NewKafkaConsumer(brokers []string, topic string, groupID string, router *llm.LLMRouter, cbClient *callback.CallbackClient, redisCache *cache.RedisCache, workerCount int, dlqTopic string, retryTopic string, maxRetry int) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     brokers,
		Topic:       topic,
		GroupID:     groupID,
		MinBytes:    10e3,
		MaxBytes:    10e6,
		StartOffset: kafka.FirstOffset,
		MaxWait:     1 * time.Second,
	})

	return &KafkaConsumer{
		Reader:         reader,
		llmRouter:      router,
		callbackClient: cbClient,
		limitChan:      make(chan struct{}, workerCount),
		workerCount:    workerCount,
		redisCache:     redisCache,
		dlqProducer:    NewDLQProducer(brokers, dlqTopic),   // 初始化死信生产者
		retryProducer:  NewDLQProducer(brokers, retryTopic), // 初始化重试生产者
		maxRetry:       maxRetry,
	}
}

func (c *KafkaConsumer) Start(ctx context.Context) {
	log.Println("🚀 Kafka 核心消费者已启动，正在监听主队列消息...")
	defer c.Close()

	var wg sync.WaitGroup

	defer func() {
		log.Println("⏳ 正在等待所有后台 AI 任务处理完毕...")
		wg.Wait()
		log.Println("✅ 所有后台任务已圆满结束")
	}()

	for {
		select {
		case c.limitChan <- struct{}{}:
		log.Println("🔓 拿到令牌，开始 Fetch...") // 加这一行
		case <-ctx.Done():
			log.Println("🛑 停止拉取新消息，等待处理中的任务...")
			return
		}

		msg, err := c.Reader.FetchMessage(ctx)
		if err != nil {
			<-c.limitChan
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("❌ 拉取消息失败: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}

		wg.Add(1)
		go func(m kafka.Message) {
			defer wg.Done()
			defer func() { <-c.limitChan }()

			// 给予绝对超时控制
			processCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			c.processMessage(processCtx, m)
		}(msg)
	}
}

// 🌟 核心处理逻辑
func (c *KafkaConsumer) processMessage(ctx context.Context, msg kafka.Message) {
	var task TaskMessage

	// ==========================================
	// 阶段 1：解析与校验 (永久错误判定)
	// ==========================================
	if err := json.Unmarshal(msg.Value, &task); err != nil {
		c.sendToDLQAndCommit(msg, "JSON解析失败(数据损坏的永久错误)", 0)
		return
	}
	
	if task.AppKeyID == 0 || task.APIKey == "" {
		c.sendToDLQAndCommit(msg, "缺少认证信息(永久错误)", 0)
		return
	}
	log.Printf("📥 [收到任务] ID: %s, 文本长度: %d 字符", task.TaskID, len(task.Content))
	// ==========================================
	// 阶段 2：限流与风控 (临时错误判定，包含 Redis 降级策略)
	// ==========================================
	rpmAllowed, _ := c.redisCache.CheckRPMLimit(ctx, task.APIKey, task.RPMLimit)
	tpmAllowed, _ := c.redisCache.CheckTPMLimit(ctx, task.APIKey, task.TPMLimit)

	if !rpmAllowed || !tpmAllowed {
		log.Printf("🚫 [限流] 租户 %s 触发流控(Redis/本地降级)，转入重试队列！", task.APIKey)
		// 🌟 绝不 Sleep！立刻扔进重试队列，交出主线程控制权
		c.sendToRetryAndCommit(msg, "触发租户级限流，等待系统额度恢复", 1)
		return
	}

	// ==========================================
	// 阶段 3：缓存拦截
	// ==========================================
	if cacheResult, ok := c.redisCache.GetCachedResponse(task.Content); ok {
		log.Printf("💰 缓存命中，直接返回！TaskID: %s", task.TaskID)
		c.handleCallback(ctx, msg, task, cacheResult, llm.Usage{})
		return
	}

	// ==========================================
	// 阶段 4：大模型调用与精细化异常处理
	// ==========================================
	var aiResult string
	var usage llm.Usage
	var err error

	// 1. 计算当前任务的 Token 数量
	tokenCount := tokenizer.CountTokens(task.Content)
	log.Printf("📊 [Token 统计] ID: %s, 计算结果: %d Tokens", task.TaskID, tokenCount)
	
	// 2. 定义大任务的阈值（比如 60,000 Tokens）
	const LargeTaskThreshold = 5000

	if tokenCount > LargeTaskThreshold {
		log.Printf("🔥 触发 Tree-Reduce 引擎！TaskID: %s, 预估 Token: %d", task.TaskID, tokenCount)
		
		instruction := "请提取核心要点并进行详细总结" 
		
		// 🌟 调用带有 Overlap 的切分器
		// 每个块 40000 Token，相邻块重叠 2000 Token (约 5% 的重叠率)console.log('::: ', );
		chunks := tokenizer.SplitTextWithOverlap(task.Content, 5000, 500)
		log.Printf("📦 任务已被切分为 %d 个子块并发处理", len(chunks))

		// 🌟 传入 redisCache 开启断点续传保护
		mrEngine := llm.NewMapReduceEngine(c.llmRouter, c.redisCache)
		aiResult, usage, err = mrEngine.ProcessLargeTask(ctx, instruction, chunks)

	} else {
		// 普通任务，走原来的单次调用逻辑
		aiResult, usage, err = c.llmRouter.InvokeWithFallback(ctx, task.Content)
	}

	if err != nil {
		errStr := err.Error()

		// 记录失败日志落库
		c.recordDBLog(task, "", usage, "fail", errStr)
		metrics.TasksTotal.WithLabelValues("fail").Inc()

		// 1. 如果是 4xx 错误 (比如 400 提示词太长、401 欠费、403 内容涉政违规)
		if strings.Contains(errStr, "状态码: 4") || strings.Contains(errStr, "invalid") {
			c.sendToDLQAndCommit(msg, "大模型拒绝请求(4xx永久错误): "+errStr, 0)
			return
		}

		// 2. 如果是 5xx 错误或超时 (比如官方 API 宕机、网络抖动)
		c.sendToRetryAndCommit(msg, "大模型网络异常(5xx临时错误): "+errStr, 1)
		return
	}

	// ==========================================
	// 阶段 5：成功落库与对账扣费
	// ==========================================
	c.recordDBLog(task, aiResult, usage, "success", "")
	c.redisCache.RecordTokenUsage(task.TaskID, usage)
	c.redisCache.IncrGlobalTokenStats(usage)
	c.redisCache.AddTPMUsage(task.APIKey, usage.TotalTokens) // 精准扣减 TPM 额度
	_ = c.redisCache.SetCachedResponse(task.Content, aiResult)

	// ==========================================
	// 阶段 6：回调主系统
	// ==========================================
	c.handleCallback(ctx, msg, task, aiResult, usage)
}

// 统一封装落库逻辑，保持主函数干净
func (c *KafkaConsumer) recordDBLog(task TaskMessage, response string, usage llm.Usage, status string, errorMsg string) {
	logEntry := model.RequestLog{
		AppKeyID:         task.AppKeyID,
		TaskID:           task.TaskID,
		Prompt:           task.Content,
		Response:         response,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		Status:           status,
		ErrorMsg:         errorMsg,
	}
	// 在异步场景下，短暂忽略 DB 的普通写入报错
	_ = model.DB.Create(&logEntry).Error
}

// 独立出回调逻辑，包含 HTTP 重试和 最终降级 DLQ
func (c *KafkaConsumer) handleCallback(ctx context.Context, msg kafka.Message, task TaskMessage, aiResult string, usage llm.Usage) {
	for i := 1; i <= c.maxRetry; i++ {
		err := c.callbackClient.SendResult(ctx, task.TaskID, aiResult)
		if err == nil {
			log.Printf("✅ 任务 [%s] 回调成功！消耗 Token: %d", task.TaskID, usage.TotalTokens)
			metrics.TasksTotal.WithLabelValues("success").Inc()
			c.commit(msg) // 🌟 最终大圆满，提交 Offset
			return
		}

		log.Printf("⚠️ 任务 [%s] 第 %d/%d 次回调主系统失败: %v", task.TaskID, i, c.maxRetry, err)
		if i < c.maxRetry {
			time.Sleep(time.Duration(1<<i) * time.Second) // 2s, 4s, 8s 的指数退避阻塞 (这里Sleep没关系，因为这已经是收尾阶段)
		}
	}

	// 🌟 主系统彻底挂了 (比如主业务服务器宕机)，这也是一种临时错误
	// 把已经拿到 AI 结果的消息扔进重试队列，防止花了钱的 AI 答案丢失！
	c.sendToDLQAndCommit(msg, "回调主系统彻底超时失败(需人工补偿)", c.maxRetry)
}

// --- 以下为 Kafka 提交与流转的核心方法 ---

func (c *KafkaConsumer) commit(msg kafka.Message) {
	commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Reader.CommitMessages(commitCtx, msg); err != nil {
		log.Printf("⚠️ 提交 Offset 失败[Offset: %d]: %v\n", msg.Offset, err)
	} else {
		log.Printf("🔖 成功提交 Offset: %d\n", msg.Offset)
	}
}

// sendToDLQAndCommit 发送至【死信队列】，用于存放永久性错误
func (c *KafkaConsumer) sendToDLQAndCommit(msg kafka.Message, reason string, retryCount int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.dlqProducer.SendToDLQ(ctx, msg, reason, retryCount); err != nil {
		log.Printf("❌ DLQ 投递失败，保留原消息 [Offset: %d]", msg.Offset)
		return
	}
	c.commit(msg)
}

// sendToRetryAndCommit 🌟 发送至【延迟重试队列】，用于存放临时性错误
func (c *KafkaConsumer) sendToRetryAndCommit(msg kafka.Message, reason string, retryCount int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.retryProducer.SendToDLQ(ctx, msg, reason, retryCount); err != nil {
		log.Printf("❌ 重试队列投递失败，保留原消息 [Offset: %d]", msg.Offset)
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
			metrics.KafkaLag.WithLabelValues(stats.Topic, "all").Set(float64(stats.Lag))
		}
	}
}

func (c *KafkaConsumer) Close() error {
	if c.dlqProducer != nil {
		c.dlqProducer.Close()
	}
	if c.retryProducer != nil {
		c.retryProducer.Close()
	}
	return c.Reader.Close()
}
