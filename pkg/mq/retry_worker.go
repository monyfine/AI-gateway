package mq

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

// StartRetryDispatcher 启动智能重试调度中心
func StartRetryDispatcher(brokers []string, retryTopic string, dlqTopic string, maxRetry int) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   retryTopic,
		GroupID: "ai_gateway_retry_dispatcher",
		MaxWait: 1 * time.Second,
	})

	// 动态路由 Writer（不绑定死 Topic）
	dynamicWriter := &kafka.Writer{
		Addr:     kafka.TCP(brokers...),
		Balancer: &kafka.LeastBytes{},
	}

	log.Printf("♻️ [重试调度中心] 已启动，监听队列: %s", retryTopic)

	go func() {
		defer reader.Close()
		defer dynamicWriter.Close()

		for {
			ctx := context.Background()
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				time.Sleep(1 * time.Second)
				continue
			}

			retryCount := getRetryCountFromHeader(msg.Headers)
			originalTopic := getHeaderValue(msg.Headers, "x-original-topic")
			failedAtStr := getHeaderValue(msg.Headers, "x-failed-at")

			// 1. 超过最大重试次数 -> 彻底放弃，送入死信队列
			if retryCount > maxRetry {
				log.Printf("💀 [重试中心] 任务已达上限(%d)，转入 DLQ。原队列: %s", maxRetry, originalTopic)
				err := dynamicWriter.WriteMessages(ctx, kafka.Message{
					Topic:   dlqTopic, // 明确写入 DLQ
					Key:     msg.Key,
					Value:   msg.Value,
					Headers: append(msg.Headers, kafka.Header{Key: "x-final-status", Value: []byte("重试次数耗尽")}),
				})
				if err == nil {
					reader.CommitMessages(ctx, msg)
				}
				continue
			}

			// 2. 指数退避延迟计算 (10s, 20s, 40s...)
			failedAt, _ := strconv.ParseInt(failedAtStr, 10, 64)
			delaySec := 5 * (1 << retryCount)
			targetTime := time.Unix(failedAt, 0).Add(time.Duration(delaySec) * time.Second)
			now := time.Now()

			if now.Before(targetTime) {
				sleepDur := targetTime.Sub(now)
				log.Printf("⏳ [重试中心] 任务等待 %v 后重新投递 (第 %d 次重试)", sleepDur, retryCount)
				time.Sleep(sleepDur)
			}

			// 3. 动态路由：从哪来回哪去
			if originalTopic == "" {
				originalTopic = dlqTopic // 兜底保护
			}

			log.Printf("♻️ [重试中心] 时间到！将任务打回主干线: [%s]", originalTopic)
			err = dynamicWriter.WriteMessages(ctx, kafka.Message{
				Topic:   originalTopic, // 🌟 核心：原路返回
				Key:     msg.Key,
				Value:   msg.Value,     // 纯净的 TaskMessage
				Headers: msg.Headers,   // 原样透传带有最新 RetryCount 的 Header
			})

			if err != nil {
				log.Printf("❌ [重试中心] 打回原队列失败: %v", err)
				continue // 写入失败不 Commit，留在重试队列下次继续
			}

			// 成功投递，清理重试队列位点
			reader.CommitMessages(ctx, msg)
		}
	}()
}