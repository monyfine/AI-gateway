package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// LoadConfig loads environment variables from the local .env file when present.
func LoadConfig() {
	if err := godotenv.Load(); err != nil {
		log.Println("warning: .env file not found, using system env")
	}
}

// GetEnv returns the configured value or the provided default.
func GetEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
