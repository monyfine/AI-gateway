package task

import (
	"ai-gateway/internal/model"
	"log"
	"time"
)

func StartLogCleaner(retainDays int) {
	log.Printf("🧹 启动后台日志清理任务，保留最近 %d 天的日志", retainDays)
	ticker := time.NewTicker(24 * time.Hour)
	go func() {
		for range ticker.C {
			deadline := time.Now().AddDate(0, 0, -retainDays)
			// 直接硬删，释放磁盘空间
			model.DB.Unscoped().Where("created_at < ?", deadline).Delete(&model.RequestLog{})
			log.Printf("🗑️ 日志清理完成，删除了 [%s] 之前的记录", deadline.Format("2006-01-02"))
		}
	}()
}