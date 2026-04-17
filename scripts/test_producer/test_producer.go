// --- scripts/test_producer.go ---
package main

import (
	"context"
	"encoding/json"
	"log"
	"time"
	
	"ai-gateway/pkg/tokenizer" // 🌟 引入我们刚才写的离线分词器
	"github.com/segmentio/kafka-go"
)

type TaskMessage struct {
	TaskID   string `json:"task_id"`
	AppKeyID uint   `json:"app_key_id"`
	APIKey   string `json:"api_key"`    // 🌟 新增：用于 Redis 限流的 Key
	RPMLimit int    `json:"rpm_limit"`  // 🌟 新增
	TPMLimit int    `json:"tpm_limit"`  // 🌟 新增
	Content  string `json:"content"`
}

func main() {
	// 🌟 改造 1：创建两个 Writer，分别对应快慢车道
	fastWriter := &kafka.Writer{
		Addr:     kafka.TCP("127.0.0.1:9092"),
		Topic:    "ai_task_fast",  // 快车道
		Balancer: &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		MaxAttempts:            5,
	}
	defer fastWriter.Close()

	heavyWriter := &kafka.Writer{
		Addr:     kafka.TCP("127.0.0.1:9092"),
		Topic:    "ai_task_heavy_v3", // 慢车道
		Balancer: &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		MaxAttempts:            5,
	}
	defer heavyWriter.Close()

	task := TaskMessage{
		TaskID:   "task" + time.Now().Format("150405"),
		AppKeyID: 2, 
		APIKey:   "sk-limit-user",  // 换个 Key 防止跟上面的缓存串了
		RPMLimit: 100,               
		TPMLimit: 20000,                // 🌟 极度抠门，必然触发限流
		Content:  "你把红黑树的源码一字不差的给我，在来个万字解析", 
	}
	payload, _ := json.Marshal(task)

	// 🌟 改造 2：路由分流逻辑 (Topic Routing)
	// 在投递前，使用本地 Tiktoken 预估计算量
	estimatedTokens := tokenizer.CountTokens(task.Content)
	var err error

	// 设定一个阈值，比如 4000 Tokens (根据你的大模型限制和业务决定)
	if estimatedTokens > 1 {
		log.Printf("🐢 预估消耗 %d Tokens，识别为重载任务，路由至慢车道 (heavy_topic)", estimatedTokens)
		time.Sleep(1*time.Second)
		err = heavyWriter.WriteMessages(context.Background(), kafka.Message{
			Key:   []byte(task.TaskID),
			Value: payload,
		})
	} else {
		log.Printf("🚀 预估消耗 %d Tokens，识别为轻量任务，路由至快车道 (fast_topic)", estimatedTokens)
		err = fastWriter.WriteMessages(context.Background(), kafka.Message{
			Key:   []byte(task.TaskID),
			Value: payload,
		})
	}

	if err != nil {
		log.Fatal("❌ 发送消息失败:", err)
	}
	log.Println("✅ 任务投递成功")
}