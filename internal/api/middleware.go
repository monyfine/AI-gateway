package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/metrics" 
	"ai-gateway/pkg/cache"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync" // 🌟 新增
	"time" // 🌟 新增
	"math/rand"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)
type cachedApp struct {
	App       model.AppKey
	ExpiresAt time.Time
}

var (
	authCache sync.Map
	sfGroup   singleflight.Group // 🌟 新增：用于防缓存击穿
)

// 提取一个生成随机过期时间的辅助函数
func generateTTL() time.Time {
	// 基础 5 分钟 + 随机 0~60 秒的波动
	jitter := time.Duration(rand.Intn(60)) * time.Second
	return time.Now().Add(5*time.Minute + jitter)
}

// AuthMiddleware 校验请求头中的 Authorization: Bearer <API_KEY>
func AuthMiddleware(redisCache *cache.RedisCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 请求头"})
			return
		}

		apiKey := strings.TrimPrefix(authHeader, "Bearer ")
		var app model.AppKey
		needQueryDB := true

		// 1. 先查本地缓存
		if val, ok := authCache.Load(apiKey); ok {
			cached := val.(cachedApp)
			
			if time.Now().Before(cached.ExpiresAt) {
				app = cached.App
				needQueryDB = false
				
				// 🌟 进阶改造：阈值滑动续期
				// 假设总有效期是 5 分钟。只有当剩余有效期不足 2 分钟时，才给它续命。
				// 这样 1000 个并发请求过来，只要它还处于充足的有效期内，就不会触发任何 Store 写入！
				if time.Until(cached.ExpiresAt) < 2*time.Minute {
					authCache.Store(apiKey, cachedApp{
						App:       app,
						ExpiresAt: generateTTL(), // 加上随机防雪崩时间
					})
				}
			} else {
				// 主动清理已过期的旧数据
				authCache.Delete(apiKey)
			}
		}

		// 2. 如果缓存未命中或已过期，使用 singleflight 合并并发请求
		if needQueryDB {
			// 🌟 改造点：不管有多少个并发请求到达这里，同一个 apiKey 同一时刻只会执行一次内部的函数
			v, err, _ := sfGroup.Do(apiKey, func() (interface{}, error) {
				var dbApp model.AppKey
				if err := model.DB.Where("`key` = ? AND status = 1", apiKey).First(&dbApp).Error; err != nil {
					return nil, err
				}
				// 查到了，存入本地缓存，设置 5 分钟过期
				authCache.Store(apiKey, cachedApp{
					App:       dbApp,
					ExpiresAt: generateTTL(), // 加上随机防雪崩时间
				})
				return dbApp, nil
			})

			if err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "无效的 API Key 或已被禁用"})
				return
			}
			app = v.(model.AppKey)
		}
		
		// 1. 校验 RPM (请求频率)
		rpmAllowed, err := redisCache.CheckRPMLimit(c.Request.Context(), app.Key, app.RPMLimit)
		if err != nil {
			log.Printf("RPM 限流系统异常: %v", err)
		}
		if !rpmAllowed {
			metrics.RateLimitTotal.WithLabelValues(app.AppName,"rpm").Inc()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "请求过于频繁，已触发租户级 RPM 限流",
			})
			return
		}

		// 2. 校验 TPM (Token 消耗频率)
		tpmAllowed, err := redisCache.CheckTPMLimit(c.Request.Context(), app.Key, app.TPMLimit)
		if err != nil {
			log.Printf("TPM 限流系统异常: %v", err)
		}
		if !tpmAllowed {
			metrics.RateLimitTotal.WithLabelValues(app.AppName, "tpm").Inc()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Token 消耗过快，已触发租户级 TPM 限流",
			})
			return
		}
		// 把查到的应用信息存入 Context，后面的 Handler 可以用
		c.Set("app_info", app)
		c.Next()
	}
}

// 新增一个全局监控中间件，用来记录所有 API 的请求量和状态码
func PrometheusMiddleware() gin.HandlerFunc{
	return func(c *gin.Context) {
		c.Next()// 先执行后面的业务逻辑

		// 业务执行完后，获取状态码并记录
		status := strconv.Itoa(c.Writer.Status())
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		metrics.APIRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
	}
}