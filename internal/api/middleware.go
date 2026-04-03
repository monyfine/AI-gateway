package api

import (
	"ai-gateway/internal/model"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware 校验请求头中的 Authorization: Bearer <API_KEY>
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 请求头"})
			c.Abort()
			return
		}

		apiKey := strings.TrimPrefix(authHeader, "Bearer ")

		// 去数据库查这个 Key 是否合法
		var app model.AppKey

		if err := model.DB.Where("`key` = ? AND status = 1", apiKey).First(&app).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的 API Key 或已被禁用"})
			c.Abort()
			return
		}

		// 把查到的应用信息存入 Context，后面的 Handler 可以用
		c.Set("app_info", app)
		c.Next()
	}
}
