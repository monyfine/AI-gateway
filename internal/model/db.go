package model

import (
	"ai-gateway/config"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"golang.org/x/crypto/bcrypt"
)

var DB *gorm.DB

// 1. 新增普通租户表
type TenantUser struct {
	ID        uint   `gorm:"primarykey"`
	Username  string `gorm:"type:varchar(50);uniqueIndex;not null"`
	Password  string `gorm:"type:varchar(255);not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Pricing struct {
	ID              uint    `gorm:"primarykey"`
	ModelName       string  `gorm:"type:varchar(50);uniqueIndex;not null" json:"model_name"`
	PromptPrice     float64 `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
}
// 1. 管理员表 (用于后续 JWT 登录控制台)
type AdminUser struct {
	ID        uint   `gorm:"primarykey"`
	Username  string `gorm:"type:varchar(50);uniqueIndex;not null"`
	Password  string `gorm:"type:varchar(255);not null;comment:哈希加密后的密码"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// 2. 租户/应用密钥表 (API Key 管理)
type AppKey struct {
	ID        uint   `gorm:"primarykey"`
	UserID    uint   `gorm:"index;comment:所属租户ID"`
	AppName   string `gorm:"type:varchar(100);not null;comment:应用名称"`
	Key       string `gorm:"type:varchar(64);uniqueIndex;not null;comment:API_KEY (sk-开头)"`
	Status    int    `gorm:"type:tinyint;default:1;comment:状态 1正常 0禁用"`
	
	RPMLimit  int    `gorm:"comment:每分钟限制请求数"`
	TPMLimit  int    `gorm:"comment:每分钟限制Token数"`
	Balance   int64  `gorm:"type:bigint;default:0;comment:账户余额(按Token计费使用)"` // 🆕 新增：用于商业化计费

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// 3. 渠道表 (动态管理 OpenAI, DeepSeek, 阿里千问等上游)
type Channel struct {
	ID        uint   `gorm:"primarykey"`
	Name      string `gorm:"type:varchar(50);not null;comment:渠道名称"`
	Type      string `gorm:"type:varchar(20);not null;comment:渠道类型(openai, deepseek等)"`
	BaseURL   string `gorm:"type:varchar(255);not null;comment:API基础地址"`
	Key       string `gorm:"type:varchar(255);not null;comment:上游API Key"`
	Models    string `gorm:"type:text;not null;comment:该渠道支持的模型列表(逗号分隔)"`
	Weight    int    `gorm:"type:int;default:1;comment:负载均衡权重"`
	Status    int    `gorm:"type:tinyint;default:1;comment:状态 1正常 0禁用 2熔断"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// 4. 请求日志表 (保留，去掉了 TaskID，因为不再是异步任务)
type RequestLog struct {
	ID               uint   `gorm:"primarykey"`
	AppKeyID         uint   `gorm:"index;comment:关联的AppKey"`
	Prompt           string `gorm:"type:text;comment:用户提问"`
	Response         string `gorm:"type:text;comment:AI回答"`
	PromptTokens     int    `gorm:"comment:提示词消耗"`
	CompletionTokens int    `gorm:"comment:生成词消耗"`
	TotalTokens      int    `gorm:"comment:总消耗"`
	Status           string `gorm:"type:varchar(20);comment:状态 success/fail"`
	ErrorMsg         string `gorm:"type:varchar(255);comment:错误信息"`
	CreatedAt        time.Time
}

func InitDB() {
	dsn := config.GetEnv("DB_DSN", "")
	if dsn == "" {
		log.Fatal("❌ 数据库配置 DB_DSN 为空")
	}

	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
		Logger:                 logger.Default.LogMode(logger.Error),
	})

	if err != nil {
		log.Fatalf("❌ 连接数据库失败: %v", err)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("❌ 获取底层 SQL 句柄失败: %v", err)
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(30 * time.Minute)

	// 🌟 填空点：在这里把上面定义的 4 个结构体指针填进去，让 GORM 自动建表
	err = DB.AutoMigrate(
		&AdminUser{}, &AppKey{}, &Channel{}, &RequestLog{},&Pricing{},&TenantUser{},
	)
	if err != nil {
		log.Fatalf("❌ 自动建表失败: %v", err)
	}
	log.Println("✅ 数据库连接成功，连接池已配置，表结构已同步")
	
	var count int64
	DB.Model(&AdminUser{}).Count(&count)
	if count == 0{
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte("admin123"),bcrypt.DefaultCost)
		if err != nil{
			log.Fatalf("❌ 密码加密失败: %v", err)
		}
		admin := AdminUser{
			Username: "admin",
			Password: string(hashedPassword),
		}
		DB.Create(&admin)
		log.Println("🎉 检测到初次启动，已自动创建默认管理员账号: admin / admin123")
	}
}