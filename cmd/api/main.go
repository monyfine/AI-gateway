package main

import (
	"ai-gateway/config"
	"ai-gateway/internal/api"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/llm"
	"log"
	"os"
)

func main() {
	log.Println("🚀 正在启动 AI Gateway API 服务...")
	// 1. 加载配置
	config.LoadConfig()

	// 🌟 加这一行调试代码：
	log.Println("【DEBUG】当前读到的 DB_DSN 是:", os.Getenv("DB_DSN"))

	// 2. 初始化数据库
	model.InitDB()
	// 3. 初始化 LLM 路由器 (复用原来的代码)
	primaryProvider := llm.NewBaseClient(
		config.GetEnv("LLM_PRIMARY_NAME", "PrimaryLLM"),
		config.GetEnv("LLM_PRIMARY_URL", ""),
		config.GetEnv("LLM_PRIMARY_KEY", ""),
		config.GetEnv("LLM_PRIMARY_MODEL", ""),
	)
	llmRouter := llm.NewLLMRouter(primaryProvider)

	// 4. 初始化 Gin 路由
	r := api.SetupRouter(llmRouter)

	// 5. 启动 HTTP 服务
	port := config.GetEnv("API_PORT", ":8080")
	log.Printf("✅ API 服务已启动，监听端口 %s", port)
	if err := r.Run(port); err != nil {
		log.Fatalf("❌ 服务启动失败: %v", err)
	}
}
