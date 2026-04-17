package config

import (
	"log"
	"os"
	"fmt"

	"github.com/joho/godotenv"
)

// LoadConfig 加载配置
func LoadConfig() {
	// 加载 .env 文件到环境变量
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found, using system env")
	}
}

// GetEnv 获取配置，支持默认值
func GetEnv(key, defaultValue string) string {
	fmt.Println("fsdfsf")
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
