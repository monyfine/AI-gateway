package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/tokenizer"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type ChatRequest struct {
	Model  string `json:"model" binding:"required"`
	Prompt string `json:"prompt" binding:"required"`
	Stream bool   `json:"stream"`
}

// ChatHandler 处理同步的 AI 请求 (网关核心)
func ChatHandler(routerManager *llm.RouterManager, redisCache *cache.RedisCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: prompt 不能为空"})
			return
		}

		appInfo, _ := c.Get("app_info")
		app := appInfo.(model.AppKey)
		ok, err := model.CheckBalance(app.ID)
		if err != nil {
			log.Printf("❌ 余额校验发生内部错误: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "服务内部错误，请稍后再试"})
			return
		}
		if !ok {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": "账户余额不足，请充值"})
			return
		}
		
		router := routerManager.Get()
		// ==========================================
		// 🌟 场景 A：前端请求流式输出 (Stream: true)
		// ==========================================
		if req.Stream {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("Connection", "keep-alive")
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")

			if cachedResult, ok := redisCache.GetCachedResponse(req.Prompt); ok {
				c.SSEvent("message", cachedResult)
				c.SSEvent("message", "[DONE]")
				return
			}

			ch, err := router.InvokeStreamWithFallback(c.Request.Context(), req.Model, req.Prompt)
			if err != nil {
				c.SSEvent("error", err.Error())
				return
			}

			var fullResponse strings.Builder
			var finalUsage llm.Usage

			promptTokens := tokenizer.CountTokens(req.Prompt)
			currentTotalTokens := promptTokens
			isInterrupted := false // 标记是否被网关强制阻断

			c.Stream(func(w io.Writer) bool {
				msg, ok := <-ch
				if !ok {
					c.SSEvent("message", "[DONE]")
					return false
				}
				if msg.Content != "" {
					fullResponse.WriteString(msg.Content)
					c.SSEvent("message", msg.Content)

					chunkTokens := len(msg.Content)*6/10
					currentTotalTokens += chunkTokens
					if currentTotalTokens >= app.TPMLimit{
						log.Printf("🚫 触发实时拦截：AppKey[%d] 流式输出超限 (当前估算: %d, 限制: %d)", app.ID, currentTotalTokens, app.TPMLimit)

						c.SSEvent("error", "当前请求已达到 TPM 限制，输出被强制中断")
						c.SSEvent("message", "[DONE]")
						
						isInterrupted = true
						return  false
					}
				}
				if msg.Usage != nil {
					finalUsage = *msg.Usage
				}
				return true
			})

			finalText := fullResponse.String()
			// 异步记录日志与扣费
			go func() {
				var total, promptTokens, compTokens int
				var status string

				if !isInterrupted && finalUsage.TotalTokens > 0 {
					promptTokens = finalUsage.PromptTokens // 覆盖为底层精确值
					compTokens = finalUsage.CompletionTokens
					total = finalUsage.TotalTokens
					status = "success_stream"
				} else {
					// 走到这里说明：被强制掐断了，或者底层大模型没返回 Usage
					// 利用 tokenizer 重新精确计算实际生成的字符 (finalText)
					compTokens = tokenizer.CountTokens(finalText)
					total = promptTokens + compTokens
					
					if isInterrupted {
						status = "interrupted_stream" // 记录为被阻断状态
					} else {
						status = "success_stream_no_usage"
					}
				}

				logEntry := model.RequestLog{
					AppKeyID:         app.ID,
					Prompt:           req.Prompt,
					Response:         finalText,
					PromptTokens:     promptTokens,
					CompletionTokens: compTokens,
					TotalTokens:      total,
					Status:           status,
				}
				model.DB.Create(&logEntry)
				redisCache.AddSlidingTPMUsage(app.Key, total)

				if status == "success_stream" {
					_ = redisCache.SetCachedResponse(req.Prompt, finalText)
				}
				deductErr := model.DeductBalance(app.ID, req.Model, promptTokens, compTokens)
				if deductErr != nil {
					log.Printf("🚨 流式调用异步扣费失败: appKeyID=%d, model=%s, err=%v", app.ID, req.Model, deductErr)
				}
			}()
			return
		}

		// ==========================================
		// 🌟 场景 B：非流式输出 (同步等待结果，不再发 Kafka)
		// ==========================================
		// 1. 先查缓存
		if cachedResult, ok := redisCache.GetCachedResponse(req.Prompt); ok {
			log.Printf("💰 [同步接口] 缓存命中，0 延迟返回！")
			model.DB.Create(&model.RequestLog{
				AppKeyID: app.ID,
				Prompt:   req.Prompt,
				Response: cachedResult,
				Status:   "success_cached",
			})
			c.JSON(http.StatusOK, gin.H{
				"content": cachedResult,
				"usage":   llm.Usage{},
				"cached":  true,
			})
			return
		}

		// 2. 同步调用大模型
		aiResult, usage, err := router.InvokeWithFallback(c.Request.Context(), req.Model, req.Prompt)
		if err != nil {
			log.Printf("❌ AI 调用失败: %v", err)
			model.DB.Create(&model.RequestLog{
				AppKeyID: app.ID,
				Prompt:   req.Prompt,
				Status:   "fail",
				ErrorMsg: err.Error(),
			})
			c.JSON(http.StatusInternalServerError, gin.H{"error": "AI 服务暂时不可用", "details": err.Error()})
			return
		}

		// 3. 异步记录成功日志并扣费
		go func() {
			model.DB.Create(&model.RequestLog{
				AppKeyID:         app.ID,
				Prompt:           req.Prompt,
				Response:         aiResult,
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
				Status:           "success",
			})
			redisCache.AddSlidingTPMUsage(app.Key, usage.TotalTokens)
			_ = redisCache.SetCachedResponse(req.Prompt, aiResult)

			deductErr := model.DeductBalance(app.ID, req.Model, usage.PromptTokens, usage.CompletionTokens)
			if deductErr != nil {
				log.Printf("🚨 同步调用异步扣费失败: appKeyID=%d, model=%s, err=%v", app.ID, req.Model, deductErr)
			}
		}()

		// 4. 立刻返回结果给前端
		c.JSON(http.StatusOK, gin.H{
			"content": aiResult,
			"usage":   usage,
			"cached":  false,
		})
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
