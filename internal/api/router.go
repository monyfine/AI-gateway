package api

import (
	"ai-gateway/pkg/llm"
	"github.com/gin-gonic/gin"
)

// SetupRouter 初始化 Gin 路由
func SetupRouter(llmRouter *llm.LLMRouter) *gin.Engine {
	r := gin.Default()

	// 健康检查接口
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	r.POST("/api/v1/articles/callback", CallbackHandler())

	// V1 版本 API 路由组
	v1 := r.Group("/v1")
	v1.Use(AuthMiddleware()) // 🌟 挂载鉴权中间件
	{
		// 注册聊天接口，把你的 llmRouter 传进去
		v1.POST("/chat/completions", ChatHandler(llmRouter))
	}
	return r
}
