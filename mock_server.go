package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()

	// 模拟主系统的回调接收接口
	r.POST("/api/v1/articles/callback", func(c *gin.Context) {
		var req map[string]interface{}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		
		log.Printf("🎉 [主系统] 收到 AI 处理完成的回调！TaskID: %v", req["task_id"])
		log.Printf("📝 内容预览: %v\n", req["content"])

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	log.Println("📞 模拟回调服务器已启动，监听 8080 端口...")
	r.Run(":8080")
}