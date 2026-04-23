// --- START OF FILE cmd/api/main.go ---
package main

import (
	"ai-gateway/config"
	"ai-gateway/internal/api"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"
	"context"
	"log"
	"net/http"  
	"os"        
	"os/signal" 
	"syscall"  
	"time"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	
	// 启动 9091 监控专属端口
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		log.Println("📊 监控服务已启动，监听端口 :9091")
		if err := http.ListenAndServe(":9091", mux); err != nil {
			log.Fatalf("❌ 监控服务启动失败: %v", err)
		}
	}()
	
	// 1. 创建原生的 HTTP Server
	srv := &http.Server{
		Addr:    port,
		Handler: r,
	}
	go func() {
		log.Printf("✅ API 服务已启动，监听端口 %s", port)
		// ErrServerClosed 是正常关闭的信号，不需要报错
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ 服务启动失败: %v", err)
		}
	}()

	// 3. 设置系统信号监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("⚠️ 接收到关闭信号，准备优雅停机，不再接收新请求...")

	// 4. 设置 10 秒钟的超时时间，给正在处理的请求收尾
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("😱 API 服务强制退出:", err)
	}

	log.Println("✅ API 服务已安全、平滑地退出")
}
