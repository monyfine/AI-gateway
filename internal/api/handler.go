package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/mq"
	"ai-gateway/pkg/tokenizer"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

type ChatRequest struct {
	Prompt string `json:"prompt" binding:"required"`
	Stream bool   `json:"stream"`
}

func sendTaskToKafka(ctx context.Context, brokers []string, topic string, task mq.TaskMessage) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}

	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: kafka.RequireAll,
	}
	defer writer.Close()

	return writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(task.TaskID),
		Value: payload,
	})
}

// ChatHandler 处理同步的 AI 请求
func ChatHandler(router *llm.LLMRouter, redisCache *cache.RedisCache, brokers []string, fastTopic string, heavyTopic string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: prompt 不能为空"})
			return
		}

		appInfo, _ := c.Get("app_info")
		app := appInfo.(model.AppKey)
		taskID := uuid.New().String()

		// ==========================================
		// 🌟 场景 A：前端请求流式输出 (Stream: true)
		// ==========================================
		fmt.Println(req.Stream)
		if req.Stream {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("Connection", "keep-alive")

			if cachedResult, ok := redisCache.GetCachedResponse(req.Prompt); ok {
				c.SSEvent("message", cachedResult)
				c.SSEvent("message", "[DONE]")
				return
			}

			ch, err := router.InvokeStreamWithFallback(c.Request.Context(), req.Prompt)
			if err != nil {
				c.SSEvent("error", err.Error())
				return
			}

			var fullResponse strings.Builder
			var finalUsage llm.Usage

			c.Stream(func(w io.Writer) bool {
				msg, ok := <-ch
				if !ok {
					c.SSEvent("message", "[DONE]")
					return false
				}
				if msg.Content != "" {
					fullResponse.WriteString(msg.Content)
					c.SSEvent("message", msg.Content)
				}
				if msg.Usage != nil {
					finalUsage = *msg.Usage
				}
				return true
			})

			finalText := fullResponse.String()
			go func() {
				var total, promptTokens, compTokens int
				var status string

				if finalUsage.TotalTokens > 0 {
					promptTokens = finalUsage.PromptTokens
					compTokens = finalUsage.CompletionTokens
					total = finalUsage.TotalTokens
					status = "success_stream"
				} else {
					promptTokens = tokenizer.CountTokens(req.Prompt)
					compTokens = tokenizer.CountTokens(finalText)
					total = promptTokens + compTokens
					status = "interrupted_stream"
				}

				logEntry := model.RequestLog{
					AppKeyID:         app.ID,
					TaskID:           taskID,
					Prompt:           req.Prompt,
					Response:         finalText,
					PromptTokens:     promptTokens,
					CompletionTokens: compTokens,
					TotalTokens:      total,
					Status:           status,
				}
				model.DB.Create(&logEntry)
				redisCache.AddTPMUsage(app.Key, total)

				if status == "success_stream" {
					_ = redisCache.SetCachedResponse(req.Prompt, finalText)
				}
			}()
			return
		}

		// ==========================================
		// 🌟 场景 B：非流式输出 (改造为异步投递到 Kafka)
		// ==========================================
		// 1. 先查缓存，如果有直接返回
		if cachedResult, ok := redisCache.GetCachedResponse(req.Prompt); ok {
			log.Printf("💰 [同步接口] 缓存命中，0 延迟返回！")

			logEntry := model.RequestLog{
				AppKeyID: app.ID,
				TaskID:   taskID,
				Prompt:   req.Prompt,
				Response: cachedResult,
				Status:   "success_cached",
			}
			model.DB.Create(&logEntry)

			c.JSON(http.StatusOK, gin.H{
				"task_id": logEntry.TaskID,
				"content": cachedResult,
				"usage":   llm.Usage{},
				"cached":  true,
			})
			return
		}

		// 2. 构造要发往 Kafka 的任务消息
		taskMsg := mq.TaskMessage{
			TaskID:   taskID,
			AppKeyID: app.ID,
			APIKey:   app.Key,
			RPMLimit: app.RPMLimit,
			TPMLimit: app.TPMLimit,
			Content:  req.Prompt,
		}

		// 3. 智能路由：根据 Token 数量决定进哪个队列
		tokenCount := tokenizer.CountTokens(req.Prompt)
		targetTopic := fastTopic
		if tokenCount > 40 {
			targetTopic = heavyTopic
		}

		// 4. 投递到 Kafka
		err := sendTaskToKafka(c.Request.Context(), brokers, targetTopic, taskMsg)
		if err != nil {
			log.Printf("❌ 任务投递 Kafka 失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "系统繁忙，任务提交失败"})
			return
		}

		// 5. 记录一条初始状态的日志，方便后续追踪
		logEntry := model.RequestLog{
			AppKeyID: app.ID,
			TaskID:   taskID,
			Prompt:   req.Prompt,
			Status:   "pending", // 标记为排队处理中
		}
		model.DB.Create(&logEntry)

		log.Printf("📤 任务 [%s] 已成功投递至 Kafka 队列 [%s], Token预估: %d", taskID, targetTopic, tokenCount)

		// 6. 立即向前端返回 202 Accepted，告知任务已受理
		c.JSON(http.StatusAccepted, gin.H{
			"task_id": taskID,
			"status":  "processing",
			"message": "任务已提交至后台队列处理，请等待回调通知",
		})
	}
}

// CallbackHandler 模拟主系统接收 AI 处理结果的回调
func CallbackHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			TaskID  string `json:"task_id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "回调数据格式错误"})
			return
		}

		log.Printf("🔔 [主系统] 收到 AI 任务回调: TaskID=%s, 状态=%s", req.TaskID, req.Status)
		// 这里可以写你自己的业务逻辑，比如更新数据库里的文章状态等

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// StatsHandler 返回 JSON 格式的全局统计数据
func StatsHandler(redisCache *cache.RedisCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := redisCache.GetGlobalTokenStats()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计数据失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"msg":  "success",
			"data": stats,
		})
	}
}

// RetryDLQHandler 触发死信队列重试 (基于 Header 架构升级版)
func RetryDLQHandler(dlqTopic string, targetTopic string, brokers []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		reader := kafka.NewReader(kafka.ReaderConfig{
			Brokers: brokers,
			Topic:   dlqTopic,
			GroupID: "dlq_admin_recovery_group",
		})
		defer reader.Close()

		writer := &kafka.Writer{
			Addr: kafka.TCP(brokers...),
			// 注意：这里去掉了固定的 Topic，因为我们要用代码动态指定，做到“从哪来回哪去”
			RequiredAcks: kafka.RequireAll,
		}
		defer writer.Close()

		recoveredCount := 0
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for {
			msg, err := reader.ReadMessage(ctx)
			if err != nil {
				break // 读完或者 10 秒没新消息就退出
			}

			actualTarget := targetTopic
			for _, h := range msg.Headers {
				if h.Key == "x-original-topic" && len(h.Value) > 0 {
					actualTarget = string(h.Value)
					break
				}
			}

			// 给它全新的 Header，重置重试次数为 0，并打上人工干预的标记
			newHeaders := []kafka.Header{
				{Key: "x-retry-count", Value: []byte("0")},     // 清除重试历史
				{Key: "x-is-recovered", Value: []byte("true")}, // 标记为人工恢复
			}

			// 🌟 3. 直接投递：不再需要 json.Unmarshal 拆包！
			err = writer.WriteMessages(context.Background(), kafka.Message{
				Topic:   actualTarget, // 精准打回原队列 (如 ai_task_fast)
				Key:     msg.Key,
				Value:   msg.Value, // 直接原封不动把纯净的业务 JSON 发回去
				Headers: newHeaders,
			})

			if err == nil {
				recoveredCount++
			} else {
				log.Printf("❌ 人工恢复消息失败 [Offset: %d]: %v", msg.Offset, err)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message":         "死信队列补偿执行完毕",
			"recovered_count": recoveredCount,
		})
	}
}
