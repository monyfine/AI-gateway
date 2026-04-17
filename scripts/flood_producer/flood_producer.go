package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type TaskMessage struct {
	TaskID   string `json:"task_id"`
	AppKeyID uint   `json:"app_key_id"`
	APIKey   string `json:"api_key"`
	RPMLimit int    `json:"rpm_limit"`
	TPMLimit int    `json:"tpm_limit"`
	Content  string `json:"content"`
}

func main() {
	writer := &kafka.Writer{
		Addr:     kafka.TCP("127.0.0.1:9092"),
		Topic:    "ai_task_fast", // 快车道
		Balancer: &kafka.LeastBytes{},
	}
	defer writer.Close()

	totalMessages := 10000
	batchSize := 100 // 🌟 核心：每 100 条打包发一车
	batch := make([]kafka.Message, 0, batchSize)

	log.Printf("🌊 准备向 Kafka 泄洪 %d 条消息 (采用大巴车打包模式)...", totalMessages)
	start := time.Now()
	successCount := 0

	for i := 0; i < totalMessages; i++ {
		task := TaskMessage{
			TaskID:   fmt.Sprintf("flood_%d", i),
			AppKeyID: 1,
			APIKey:   "test-api-key-123", // 确保数据库里有这个Key，或者没开启强校验
			Content:  "这是一条压测消息，测试系统的极限吞吐量",
		}
		payload, _ := json.Marshal(task)

		// 不直接发，先装上车
		batch = append(batch, kafka.Message{
			Key:   []byte(task.TaskID),
			Value: payload,
		})

		// 车满了（100条），或者到了最后一条，就发车
		if len(batch) >= batchSize || i == totalMessages-1 {
			// 🌟 注意这里：一次性传入一整个切片 (batch...)
			err := writer.WriteMessages(context.Background(), batch...)
			if err != nil {
				log.Printf("❌ 第 %d 批次发车失败: %v", i/batchSize, err)
			} else {
				successCount += len(batch)
			}
			// 发完车后清空车厢
			batch = batch[:0]
		}
	}

	log.Printf("✅ 泄洪完成！总计成功发送 %d 条，耗时: %v", successCount, time.Since(start))
}