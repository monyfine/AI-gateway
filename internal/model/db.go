package model

import (
	"ai-gateway/config"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// AppKey 租户/应用密钥表 (用于 API 鉴权)
type AppKey struct {
	ID        uint   `gorm:"primarykey"`
	AppName   string `gorm:"type:varchar(100);not null;comment:应用名称"`
	Key       string `gorm:"type:varchar(64);uniqueIndex;not null;comment:API_KEY"`
	Status    int    `gorm:"type:tinyint;default:1;comment:状态 1正常 0禁用"`
	
	RPMLimit  int    `gorm:"comment:每分钟限制请求数"`
	TPMLimit  int    `gorm:"comment:每分钟限制Token数"`

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
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		// 🌟 1. 禁用默认事务（性能提升核心！）
		// GORM 默认在创建/更新时会开启事务。
		// 对于我们的 RequestLog（日志）来说，单条插入不需要事务，禁用后性能提升约 30% 以上。
		SkipDefaultTransaction: true,

		// 🌟 2. 开启缓存预编译语句
		// 开启后，执行 SQL 会快很多，因为它会缓存已经编译好的 SQL 语句。
		PrepareStmt: true,

		// 🌟 3. 日志级别控制
		// 生产环境下建议设为 Silent（静默）或 Error，防止控制台被密集的 SQL 日志刷屏导致性能下降。
		Logger: logger.Default.LogMode(logger.Error),

	})

	if err != nil {
		log.Fatalf("❌ 连接数据库失败: %v", err)
	}

	sqlDB,err := DB.DB()
	if err != nil{
		log.Fatalf("❌ 获取底层 SQL 句柄失败: %v", err)
	}
	// 🌟 3. 设置连接池参数 (核心优化)
	
	// 设置空闲连接池中连接的最大数量
	// 建议：设置为 workerCount + API并发预估，通常 10-20 比较合适
	sqlDB.SetMaxIdleConns(20)

	// 设置打开数据库连接的最大数量
	// ⚠️ 注意：这个值不能超过 MySQL 配置文件里的 max_connections (默认通常是 151)
	sqlDB.SetMaxOpenConns(100)

	// 设置连接可复用的最大时间 (防止 MySQL 的 wait_timeout 导致连接失效)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 设置连接在空闲时保持的最大时间
	sqlDB.SetConnMaxIdleTime(30 * time.Minute)

	// 自动迁移表结构 (自动建表)
	err = DB.AutoMigrate(&AppKey{}, &RequestLog{})
	if err != nil {
		log.Fatalf("❌ 自动建表失败: %v", err)
	}
	log.Println("✅ 数据库连接成功，连接池已配置，表结构已同步")
}
