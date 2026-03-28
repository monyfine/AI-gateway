package mq

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"ai-gateway/pkg/llm" // 确保你的 go.mod 里模块名是 ai-gateway

	"github.com/segmentio/kafka-go"
)

// KafkaConsumer 消费者结构体
type KafkaConsumer struct {
	Reader    *kafka.Reader
	llmClient *llm.LLMClient // 依赖注入 LLM 客户端
}

// TaskMessage 任务消息结构体
// 🚨 注意：字段首字母必须大写！使用 json tag 映射实际的 key
type TaskMessage struct {
	TaskID  string `json:"task_id"` // 建议用 string，兼容性更好
	Content string `json:"content"`
}

// NewKafkaConsumer 构造函数 (参数改为小驼峰命名)
func NewKafkaConsumer(brokers[] string, topic string, groupID string, client *llm.LLMClient) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID,
		MinBytes: 10e3, // 10KB
		MaxBytes: 10e6, // 10MB
	})
	return &KafkaConsumer{
		Reader:    reader,
		llmClient: client,
	}
}

// Start 启动消费者阻塞循环
func (c *KafkaConsumer) Start(ctx context.Context) {
	log.Println("🚀 Kafka 消费者已启动，正在监听消息...")
	defer c.Close()
	for {
		// 1. 拉取消息 (Fetch 不会提交 Offset)
		msg, err := c.Reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Println("🛑 收到退出信号，停止拉取 Kafka 消息")
				break
			}
			log.Printf("❌ 拉取消息失败: %v\n", err)
			continue
		}

		log.Printf("📥 收到消息: Topic=%s, Partition=%d, Offset=%d\n", msg.Topic, msg.Partition, msg.Offset)

		// 2. 解析 JSON
		var task TaskMessage
		err = json.Unmarshal(msg.Value, &task)
		if err != nil {
			// ☠️ 毒药消息防御：打印详细日志，并强制 Commit
			log.Printf("☠️ 毒药消息警告! JSON解析失败. Offset: %d, Error: %v, Value: %s\n", msg.Offset, err, string(msg.Value))
			c.commit(ctx, msg) // 封装一个小方法来处理 commit
			continue
		}

		// 3. 调用大模型 (核心业务逻辑)
		log.Printf("🧠 开始处理任务 [%s], 内容: %s\n", task.TaskID, task.Content)
		aiResult, err := c.llmClient.InvokeLLM(ctx, task.Content)
		if err != nil {
			// AI 调用失败：目前策略是打日志，但依然 Commit（后续可引入重试队列）
			log.Printf("⚠️ 任务 [%s] AI 调用失败: %v\n", task.TaskID, err)
		} else {
			// AI 调用成功
			log.Printf("✅ 任务 [%s] AI 处理完成. 结果: %s\n", task.TaskID, aiResult)
		}

		// 4. 业务处理完毕，提交 Offset
		c.commit(ctx, msg)
	}
}

// Close 优雅关闭 (首字母大写，供外部调用)
func (c *KafkaConsumer) Close() error {
	return c.Reader.Close()
}

// commit 内部辅助方法，减少重复代码
func (c *KafkaConsumer) commit(ctx context.Context, msg kafka.Message) {
	if err := c.Reader.CommitMessages(ctx, msg); err != nil {
		log.Printf("⚠️ 提交 Offset 失败[Offset: %d]: %v\n", msg.Offset, err)
	} else {
		log.Printf("🔖 成功提交 Offset: %d\n", msg.Offset)
	}
}