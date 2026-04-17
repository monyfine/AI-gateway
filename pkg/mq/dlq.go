package mq

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

// DLQProducer 用于向重试或死信队列发送消息
type DLQProducer struct {
	writer *kafka.Writer
	topic  string
}

func NewDLQProducer(brokers []string, topic string) *DLQProducer {
	return &DLQProducer{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topic,
			Balancer:     &kafka.LeastBytes{},
			RequiredAcks: kafka.RequireAll,
		},
		topic: topic,
	}
}

// SendToQueue 统一发送函数，利用 Header 传递重试状态
func (d *DLQProducer) SendToQueue(ctx context.Context, originalMsg kafka.Message, reason string, nextRetryCount int) error {
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 🌟 核心修改：业务数据原封不动，状态信息全部写入 Header
	// 如果 originalMsg 之前没有 x-original-topic，说明是第一次失败，我们把它当前的 Topic 记录下来
	originalTopic := getHeaderValue(originalMsg.Headers, "x-original-topic")
	if originalTopic == "" {
		originalTopic = originalMsg.Topic
	}

	// 构造新的 Headers，覆盖旧的重试信息
	headers := []kafka.Header{
		{Key: "x-retry-count", Value: []byte(strconv.Itoa(nextRetryCount))},
		{Key: "x-error-reason", Value: []byte(reason)},
		{Key: "x-original-topic", Value: []byte(originalTopic)},
		{Key: "x-failed-at", Value: []byte(strconv.FormatInt(time.Now().Unix(), 10))},
	}

	err := d.writer.WriteMessages(writeCtx, kafka.Message{
		Key:     originalMsg.Key,
		Value:   originalMsg.Value, // 纯净的业务 JSON，不加任何包装
		Headers: headers,
	})

	if err != nil {
		log.Printf("❌ 发送到队列 [%s] 失败 [Offset: %d]: %v", d.topic, originalMsg.Offset, err)
		return err
	}

	log.Printf("📤 消息已转入队列 [%s] [Offset: %d], 当前重试次数: %d, 原因: %s", d.topic, originalMsg.Offset, nextRetryCount, reason)
	return nil
}

func (d *DLQProducer) Close() error {
	return d.writer.Close()
}