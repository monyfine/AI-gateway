package api

import (
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"

	"github.com/gin-gonic/gin"
)

// SetupRouter 初始化 Gin 路由
func SetupRouter(routerManager *llm.RouterManager, redisCache *cache.RedisCache) *gin.Engine {
	r := gin.Default()

	r.StaticFile("/", "./web/index.html")
	r.StaticFile("/favicon.ico", "./web/favicon.ico") // 可选，防止浏览器报错
	
	r.StaticFile("/user", "./web/user.html")
	// 1. 挂载全局监控中间件 (记录所有请求)
	r.Use(PrometheusMiddleware())

	// 健康检查接口
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	r.GET("/api/v1/stats", StatsHandler(redisCache))

	// V1 版本 API 路由组 (数据平面)
	v1 := r.Group("/v1")
	v1.Use(AuthMiddleware(redisCache)) // 挂载 API Key 鉴权中间件
	{
		// 注册聊天接口，不再需要传入 Kafka 参数
		v1.POST("/chat/completions", ChatHandler(routerManager, redisCache))
	}

	userApi := r.Group("/api/v1/user")
	{
		userApi.POST("/register", UserRegisterHandler())
		userApi.POST("/login", UserLoginHandler())
	}

	// 🌟 新增：C 端租户专属接口 (需要 User Token)
	tenantApi := r.Group("/api/v1/tenant")
	tenantApi.Use(UserAuthMiddleware()) // 挂载你刚写的中间件
	{
		tenantApi.POST("/keys", TenantCreateAppKeyHandler())
		tenantApi.GET("/keys", TenantGetAppKeyListHandler())
		tenantApi.PUT("/keys/:id",TenantUpdateAppKeyHandler())
		tenantApi.DELETE("/keys/:id",TenantDeleteAppKeyHandler())
	}

	// 1. 创建一个名为 adminApi 的路由组，路径为 "/api/v1/admin"
	adminApi := r.Group("/api/v1/admin")
	{
		// 2. 为登录接口注册路由。它应该是 POST 方法，路径是 "/login"。
		//    这个接口不需要任何鉴权，直接调用我们写好的 AdminLoginHandler。
		adminApi.POST("/login", AdminLoginHandler())

		// 3. 在这里为 adminApi 路由组挂载我们之前写的 AdminAuthMiddleware。
		//    这样，写在这行代码下面的所有路由都会被自动保护。
		adminApi.Use(AdminAuthMiddleware())
		adminApi.POST("/keys", CreateAppKeyHandler())
		adminApi.GET("/keys", GetAppKeyListHandler())
		adminApi.PUT("/keys/:id", UpdateAppKeyHandler())
		adminApi.DELETE("/keys/:id", DeleteAppKeyHandler())
		adminApi.GET("/dashboard", GetDashboardStatsHandler())
		// 4. 创建一个受保护的路由，用于获取管理员信息。
		//    它应该是 GET 方法，路径是 "/profile"。
		adminApi.GET("/profile", GetAdminProfileHandler())
		adminApi.POST("/channels/reload", ReloadChannelsHandler(routerManager))
	}
	return r
}
