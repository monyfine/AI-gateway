// --- START OF FILE cmd/api/main.go ---
package main

import (
	"ai-gateway/config"
	"ai-gateway/internal/api"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"
	"log"
	"time"
	"net/http"  // 🌟 新增
	"os"        // 🌟 新增
	"os/signal" // 🌟 新增
	"syscall"   // 🌟 新增
	"context"   // 🌟 新增
)

func main() {
	log.Println("🚀 正在启动 AI Gateway API 服务...")
	config.LoadConfig()
	model.InitDB()

	primaryProvider := llm.NewBaseClient(
		config.GetEnv("LLM_PRIMARY_NAME", "PrimaryLLM"),
		config.GetEnv("LLM_PRIMARY_URL", ""),
		config.GetEnv("LLM_PRIMARY_KEY", ""),
		config.GetEnv("LLM_PRIMARY_MODEL", ""),
	)
	fallbackProvider := llm.NewBaseClient(
		config.GetEnv("LLM_BACKUP_NAME", "FallbackLLM"),
		config.GetEnv("LLM_BACKUP_URL", ""),
		config.GetEnv("LLM_BACKUP_KEY", ""),
		config.GetEnv("LLM_BACKUP_MODEL", ""),
	)
	llmRouter := llm.NewLLMRouter(primaryProvider, fallbackProvider)
	redisCache := cache.NewRedisCache(24 * time.Hour)
	
	r := api.SetupRouter(llmRouter, redisCache)
	port := config.GetEnv("API_PORT", ":8080")

	// 1. 创建原生的 HTTP Server
	srv := &http.Server{
		Addr:    port,
		Handler: r,
	}
	go func(){
		log.Printf("✅ API 服务已启动，监听端口 %s", port)
		// ErrServerClosed 是正常关闭的信号，不需要报错
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ 服务启动失败: %v", err)
		}
	}()
	
	// 3. 设置系统信号监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) // 捕获 Ctrl+C 和 kill 信号
	<-quit // 阻塞在这里，直到收到系统信号

	log.Println("⚠️ 接收到关闭信号，准备优雅停机，不再接收新请求...")

	// 4. 设置 10 秒钟的超时时间，给正在处理的请求收尾
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("😱 API 服务强制退出:", err)
	}

	log.Println("✅ API 服务已安全、平滑地退出")
}
/*
[发起方]
   │
   ├─► 【API 流】 cmd/api/main.go -> internal/api/router.go
   │       └── internal/api/middleware.go (AuthMiddleware: 鉴权与 Redis 限流)
   │           └── internal/api/handler.go (ChatHandler: 业务逻辑中心)
   │                   ├── 1. pkg/cache/redis.go (GetCachedResponse: 查缓存)
   │                   ├── 2. pkg/llm/router.go (InvokeWithFallback: 大模型路由与熔断)
   │                   │      └── pkg/llm/client.go (Invoke: 发起真实 HTTP 到大模型)
   │                   └── 3. internal/model/db.go & pkg/cache/redis.go (记账入库扣额度)
   │
   └─► 【MQ 流】 cmd/worker/main.go
           └── pkg/mq/consumer.go (KafkaConsumer.Start -> processMessage)
                   ├── 1. pkg/cache/redis.go (CheckRPMLimit/TPMLimit: 消费端限流)
                   ├── 2. pkg/tokenizer/tiktoken.go (CountTokens: 算长度)
                   ├── 3. 【如果太长】-> pkg/tokenizer/splitter.go (切分)
                   │                -> pkg/llm/mapreduce.go (ProcessLargeTask 分治处理)
                   │                   └── pkg/llm/router.go (大模型路由)
                   ├── 4. pkg/mq/consumer.go (handleCallback: 业务回调)
                   │      └── pkg/callback/client.go (SendResult)
                   └── 5. 【如果失败】-> pkg/mq/dlq.go (SendToDLQ: 发送重试/死信队列)
*/