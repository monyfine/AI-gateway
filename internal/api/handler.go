package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/llm"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ChatRequest struct {
	Prompt string `json:"prompt" binding:"required"`
}

// ChatHandler 处理同步的 AI 请求
func ChatHandler(router *llm.LLMRouter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: prompt 不能为空"})
			return
		}

		// 获取中间件存入的 App 信息
		appInfo, _ := c.Get("app_info")
		app := appInfo.(model.AppKey)

		aiResult, usage, err := router.InvokeWithFallback(c.Request.Context(), req.Prompt)

		logEntry := model.RequestLog{
			AppKeyID:         app.ID,
			TaskID:           uuid.New().String(), // 生成一个追踪ID
			Prompt:           req.Prompt,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		}

		if err != nil {
			logEntry.Status = "fail"
			logEntry.ErrorMsg = err.Error()
			model.DB.Create(&logEntry)

			c.JSON(http.StatusInternalServerError, gin.H{"error": "AI 处理失败", "details": err.Error()})
			return
		}

		// 成功记录
		logEntry.Status = "success"
		logEntry.Response = aiResult
		model.DB.Create(&logEntry)

		// 返回给前端
		c.JSON(http.StatusOK, gin.H{
			"task_id": logEntry.TaskID,
			"content": aiResult,
			"usage":   usage,
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
