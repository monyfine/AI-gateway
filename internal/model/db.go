package model

import (
	"ai-gateway/config"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB

// AppKey 租户/应用密钥表 (用于 API 鉴权)
type AppKey struct {
	ID        uint   `gorm:"primarykey"`
	AppName   string `gorm:"type:varchar(100);not null;comment:应用名称"`
	Key       string `gorm:"type:varchar(64);uniqueIndex;not null;comment:API_KEY"`
	Status    int    `gorm:"type:tinyint;default:1;comment:状态 1正常 0禁用"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// RequestLog 请求日志与 Token 消耗表
type RequestLog struct {
	ID               uint   `gorm:"primarykey"`
	AppKeyID         uint   `gorm:"index;comment:关联的AppKey"`
	TaskID           string `gorm:"type:varchar(64);index;comment:任务ID(可选)"`
	Prompt           string `gorm:"type:text;comment:用户提问"`
	Response         string `gorm:"type:text;comment:AI回答"`
	PromptTokens     int    `gorm:"comment:提示词消耗"`
	CompletionTokens int    `gorm:"comment:生成词消耗"`
	TotalTokens      int    `gorm:"comment:总消耗"`
	Status           string `gorm:"type:varchar(20);comment:状态 success/fail"`
	ErrorMsg         string `gorm:"type:varchar(255);comment:错误信息"`
	CreatedAt        time.Time
}

// InitDB 初始化数据库连接
func InitDB() {
	dsn := config.GetEnv("DB_DSN", "")

	if dsn == "" {
		log.Fatal("❌ 数据库配置 DB_DSN 为空")
	}

	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("❌ 连接数据库失败: %v", err)
	}

	// 自动迁移表结构 (自动建表)
	err = DB.AutoMigrate(&AppKey{}, &RequestLog{})
	if err != nil {
		log.Fatalf("❌ 自动建表失败: %v", err)
	}

	log.Println("✅ 数据库连接成功，表结构已同步")
}
