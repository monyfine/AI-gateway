package mq

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type DeadLetterMessage struct {
	OriginalMessage []byte    `json:"original_message"`
	Topic           string    `json:"topic"`
	Partition       int       `json:"partition"`
	Offset          int64     `json:"offset"`
	ErrorReason     string    `json:"error_reason"`
	FailedAt        time.Time `json:"failed_at"`
	RetryCount      int       `json:"retry_count"`
}

type DLQProducer struct {
	writer *kafka.Writer
}

func NewDLQProducer(brockers []string, dlqTopic string) *DLQProducer {
	return &DLQProducer{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brockers...),
			Topic:        dlqTopic,
			Balancer:     &kafka.LeastBytes{},
			RequiredAcks: kafka.RequireAll,
			Async:        false,
		},
	}
}

func (d *DLQProducer) SendToDLQ(ctx context.Context, originalMsg kafka.Message, reason string, retryCount int) error {
	dlqMsg := DeadLetterMessage{
		OriginalMessage: originalMsg.Value,
		Topic:           originalMsg.Topic,
		Partition:       originalMsg.Partition,
		Offset:          originalMsg.Offset,
		ErrorReason:     reason,
		FailedAt:        time.Now(),
		RetryCount:      retryCount,
	}

	payload, err := json.Marshal(dlqMsg)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err = d.writer.WriteMessages(writeCtx, kafka.Message{
		Key:   []byte(originalMsg.Key),
		Value: payload,
	})

	if err != nil {
		log.Printf("❌ 发送到 DLQ 失败 [Offset: %d]: %v", originalMsg.Offset, err)
		return err
	}
	log.Printf("📤 消息已转入死信队列 [Offset: %d], 原因: %s", originalMsg.Offset, reason)
	return nil
}

func (d *DLQProducer) Close() error {
	return d.writer.Close()
}
