package api

import (
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// SetupRouter 初始化 Gin 路由
func SetupRouter(llmRouter *llm.LLMRouter,redisCache *cache.RedisCache) *gin.Engine {
	r := gin.Default()

	// 🌟 1. 挂载全局监控中间件 (记录所有请求)
	r.Use(PrometheusMiddleware())
	// 🌟 2. 暴露指标接口给 Prometheus 抓取
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	
	// 健康检查接口
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	r.GET("/api/v1/stats", StatsHandler(redisCache))
	r.POST("/api/v1/articles/callback", CallbackHandler())
	// V1 版本 API 路由组
	v1 := r.Group("/v1")
	v1.Use(AuthMiddleware(redisCache)) // 🌟 挂载鉴权中间件
	{
		// 注册聊天接口，把你的 llmRouter 传进去
		v1.POST("/chat/completions", ChatHandler(llmRouter, redisCache)) // 传给 Handler
	}
	return r
}