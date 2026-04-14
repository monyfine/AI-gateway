package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// 必须和 consumer.go 里的结构体严格对齐
type TaskMessage struct {
	TaskID   string `json:"task_id"`
	AppKeyID uint   `json:"app_key_id"`
	APIKey   string `json:"api_key"`
	RPMLimit int    `json:"rpm_limit"`
	TPMLimit int    `json:"tpm_limit"`
	Content  string `json:"content"`
}

func main() {
	// 替换为你的 Kafka 地址
	brokers :=[]string{"localhost:9092"} 
	writer := &kafka.Writer{
		Addr:     kafka.TCP(brokers...),
		Topic:    "ai_task_heavy_v3", // 🌟🌟🌟 改成 v2 🌟🌟🌟
		Balancer: &kafka.LeastBytes{},
	}
	defer writer.Close()

	log.Println("🔥 开始生成超大文本任务...")
	
	// 伪造一篇约 15 万 Token 的超大文本
	// 一段话大概 50 个 Token，重复 3000 次，大约 15 万 Token
	baseText := "【2025年度财务报告核心片段】本季度公司营收大幅增长，各项业务指标均超出预期。我们需要大模型帮我们提取出核心要点，并进行深度总结\n\n"
	hugeContent := strings.Repeat(baseText, 300) 

	taskID := fmt.Sprintf("heavy-task-%d", time.Now().Unix())
	msg := TaskMessage{
		TaskID:   taskID,
		AppKeyID: 1,
		APIKey:   "test-stress-key-123", // 确保数据库里有这个 Key
		RPMLimit: 100000,                // 给个极大的限流值防止被拦截
		TPMLimit: 100000000,
		Content:  hugeContent,
	}

	payload, _ := json.Marshal(msg)
	
	err := writer.WriteMessages(context.Background(), kafka.Message{
		Key:[]byte(taskID),
		Value: payload,
	})

	if err != nil {
		log.Fatalf("❌ 发送失败: %v", err)
	}
	
	log.Printf("✅ 超大任务 [%s] 发送完毕！请去 Worker 控制台观察 Map-Reduce 引擎表现！", taskID)
}