package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/llm"
	"ai-gateway/pkg/utils"
	"errors"
	"net/http"
	"strconv"
	"time"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// AdminLoginHandler 负责处理管理员的登录请求
func AdminLoginHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 2：
		// 在这里实现完整的登录逻辑。
		// 请遵循以下步骤：
		//
		// 1. 定义一个 LoginRequest 类型的变量。
		var req LoginRequest
		// 2. 使用 c.ShouldBindJSON() 将请求体中的 JSON 绑定到这个变量上。
		//    - 如果绑定失败，应返回 400 Bad Request 状态码和错误信息。
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数格式错误或缺失必填字段"})
			return // 发生错误时，一定要 return 提前结束请求
		}
		// 3. 声明一个 model.AdminUser 类型的变量。
		var admin model.AdminUser
		// 4. 使用 model.DB.Where("username = ?", ...).First(...) 的方式，
		//    根据绑定的用户名去数据库里查找对应的管理员记录。
		//    - 如果 GORM 返回的错误是 gorm.ErrRecordNotFound，
		//      说明用户不存在，应返回 401 Unauthorized 状态码和 "用户不存在" 的信息。
		//    - 如果是其他数据库错误，应返回 500 Internal Server Error。
		if err := model.DB.Where("username = ?", req.Username).First(&admin).Error; err != nil {
			// 判断是否是“找不到记录”的特定错误
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
				return
			}
			// 其他数据库层面的严重错误
			c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库内部错误"})
			return
		}

		// 5. 使用 bcrypt.CompareHashAndPassword() 函数，比较用户提交的明文密码
		//    和数据库中存储的哈希密码是否匹配。
		//    - 如果不匹配，应返回 401 Unauthorized 状态码和 "密码错误" 的信息。
		if err := bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(req.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
			return
		}
		// 6. 如果密码校验通过，调用我们之前写的 utils.GenerateToken() 函数，
		//    为该用户生成一个新的 JWT。
		//    - 如果 token 生成失败，返回 500 Internal Server Error。
		token, err := utils.GenerateToken(req.Username, "admin", 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "系统生成令牌失败"})
			return
		}
		// 7. 最后，返回 200 OK 状态码，并在 JSON 响应体中包含生成的 token，
		//    格式为：{"token": "xxxxxx..."}
		c.JSON(http.StatusOK, gin.H{
			"message": "登录成功",
			"token":   token,
		})
	}
}

// GetAdminProfileHandler 获取当前登录管理员的信息
func GetAdminProfileHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 2：
		// 我们的 AdminAuthMiddleware 在校验成功后，会通过 c.Set("admin_username", ...)
		// 将用户名存入了 Gin 的 Context 中。
		// 在这里，你需要：
		// 1. 使用 c.Get("admin_username") 来获取这个值。
		//    - c.Get 返回两个值：值 (interface{}) 和 是否存在 (bool)。
		//    - 你需要做一个类型断言，将 interface{} 转换为 string。
		// 2. 如果成功获取到用户名，就返回 200 OK，以及 JSON 数据：
		//    {"username": "获取到的用户名"}
		// 3. 如果因为某种原因没有获取到（比如中间件没挂上），
		//    则返回 500 Internal Server Error 和错误信息 "无法获取用户信息"。
		usernameInterface, exists := c.Get("admin_username")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
			return
		}
		username, ok := usernameInterface.(string)
		if !ok {
			// 如果取出来的值不是字符串类型，也返回 500
			c.JSON(http.StatusInternalServerError, gin.H{"error": "用户信息格式异常"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"username": username,
		})
	}
}

type CreateAppKeyRequest struct {
	// 添加了 json 标签和 required 校验
	AppName  string `json:"app_name" binding:"required"`
	RPMLimit int    `json:"rpm_limit" binding:"required"`
	TPMLimit int    `json:"tpm_limit" binding:"required"`
}

func CreateAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 1.2：
		// - 绑定 CreateAppKeyRequest 参数。
		// - 生成一个新的 API Key。Key 的格式通常是 "sk-" + 一串随机字符串。
		//   (提示：可以用 "sk-" + uuid.New().String() 来生成
		// - 构造 model.AppKey 结构体。
		// - 使用 model.DB.Create() 存入数据库。
		// - 返回 201 Created 状态码和新创建的 AppKey 对象。
		var req CreateAppKeyRequest
		if err := c.ShouldBindBodyWithJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误或缺失必填项"})
			return
		}

		newKey := "sk-" + uuid.New().String()
		appKey := model.AppKey{
			AppName:  req.AppName,
			Key:      newKey,
			RPMLimit: req.RPMLimit,
			TPMLimit: req.TPMLimit,
			Balance:  100000,
		}
		if err := model.DB.Create(&appKey).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建 AppKey 失败"})
			return
		}
		c.JSON(http.StatusCreated, appKey)
	}
}

func GetAppKeyListHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 2.1：
		// - 从 URL 查询参数中获取 "page" 和 "size"。
		//   (提示：使用 c.Query("page")，如果获取不到，给它一个默认值，比如 page=1, size=10)
		//   (注意：c.Query 返回的是字符串，你需要用 strconv.Atoi 转换成整数)
		// - 计算 offset，公式是 (page - 1) * size。
		// - 使用 GORM 的 .Offset(offset).Limit(size).Find(&appKeys) 来实现分页查询。
		// - 同时，使用 .Count(&total) 来获取总记录数。
		// - 返回 200 OK，JSON 数据中应包含列表 `list` 和总数 `total`。
		pageStr := c.DefaultQuery("page", "1")
		sizeStr := c.DefaultQuery("size", "10")

		page, err := strconv.Atoi(pageStr)
		if err != nil || page < 1 {
			page = 1
		}
		size, err := strconv.Atoi(sizeStr)
		if err != nil || size < 10 {
			size = 10
		}
		offset := (page - 1) * size

		var appKeys []model.AppKey
		var total int64

		model.DB.Model(&model.AppKey{}).Count(&total)

		if err := model.DB.Offset(offset).Limit(size).Find(&appKeys).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取列表失败"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"list":  appKeys,
			"total": total,
		})
	}
}

type UpdateAppKeyRequest struct {
	// 题目 3.1：
	// 定义更新时允许修改的字段：
	// AppName, RPMLimit, TPMLimit, Status (int)
	// 这些字段都不是必填的，因为用户可能只更新其中一个。
	AppName  string `json:"app_name"`
	RPMLimit int    `json:"rpm_limit"`
	TPMLimit int    `json:"tpm_limit"`
	Status   int    `json:"status"`
}

func UpdateAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 3.2：
		// - 从 URL 路径参数中获取要更新的 AppKey 的 ID。
		//   (提示：比如路由是 /keys/:id，用 c.Param("id") 获取)
		// - 绑定 UpdateAppKeyRequest 参数。
		// - 先用 .First(&appKey, id) 检查这条记录是否存在。
		// - 如果存在，使用 .Model(&appKey).Updates(updateData) 来更新。
		//   (注意：.Updates 只会更新非零值的字段，正好符合我们的需求)
		// - 返回 200 OK 和更新后的 AppKey 对象。
		id := c.Param("id")
		var req UpdateAppKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误"})
			return
		}

		var appKey model.AppKey
		if err := model.DB.First(&appKey, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "找不到该 AppKey 记录"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库错误"})
			return
		}
		updateData := map[string]interface{}{
			"app_name":  req.AppName,
			"rpm_limit": req.RPMLimit,
			"tpm_limit": req.TPMLimit,
			"status":    req.Status, 
		}		
		if err := model.DB.Model(&appKey).Updates(updateData).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}
		c.JSON(http.StatusOK, appKey)
	}
}

func DeleteAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 4.1：
		// - 从 URL 路径参数中获取要删除的 AppKey 的 ID。
		// - 使用 model.DB.Delete(&model.AppKey{}, id) 执行删除。
		//   (因为我们的 AppKey 模型里有 gorm.DeletedAt，所以这会是软删除)
		// - 返回 204 No Content 状态码，表示删除成功且没有内容返回。
		id := c.Param("id")
		if err := model.DB.Delete(&model.AppKey{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

type DashboardStats struct {
	TotalRequests   int64 `json:"total_requests"`
	SuccessRequests int64 `json:"success_requests"`
	TotalTokens     int64 `json:"total_tokens"`
	ActiveAppKeys   int64 `json:"active_app_keys"`
}

func GetDashboardStatsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 获取今天 00:00:00 的时间对象
		now := time.Now()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

		var stats DashboardStats

		// 题目 1：统计今日总请求数 (查询 RequestLog 表，CreatedAt >= todayStart)
		// model.DB.Model(&model.RequestLog{}).Where(...).Count(&stats.TotalRequests)
		if err := model.DB.Model(&model.RequestLog{}).
			Where("created_at >= ?", todayStart).
			Count(&stats.TotalRequests).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取总请求数失败"})
			return
		}
		// 题目 2：统计今日成功请求数 (状态以 "success" 开头，比如 success, success_stream)
		// 提示：使用 LIKE 'success%'
		if err := model.DB.Model(&model.RequestLog{}).
			Where("created_at >= ?", todayStart).
			Where("status LIKE ?", "success%").
			Count(&stats.SuccessRequests).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取成功请求数失败"})
			return
		}
		// 题目 3：统计今日消耗总 Token 数
		// 提示：使用 GORM 的 Select("COALESCE(SUM(total_tokens), 0)").Row().Scan(&stats.TotalTokens)
		// COALESCE 是为了防止今天一条记录都没有时 SUM 返回 NULL 导致报错
		if err := model.DB.Model(&model.RequestLog{}).
			Where("created_at >= ?", todayStart).
			Select("COALESCE(SUM(total_tokens), 0)").
			Row().Scan(&stats.TotalTokens); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取总 Token 数失败"})
			return
		}
		// 题目 4：统计今日活跃的 AppKey 数量 (去重统计 AppKeyID)
		// 提示：使用 Select("COUNT(DISTINCT app_key_id)").Row().Scan(&stats.ActiveAppKeys)
		if err := model.DB.Model(&model.RequestLog{}).
			Where("created_at >= ?", todayStart).
			Select("COUNT(DISTINCT app_key_id)").
			Row().Scan(&stats.ActiveAppKeys); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取活跃 AppKey 数量失败"})
			return
		}
		// 5. 返回 200 OK 和 stats 对象
		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"msg":  "success",
			"data": stats,
		})
	}
}
// InitDynamicLLMRouter 从数据库动态加载渠道配置，初始化路由
func BuildDynamicRouter() *llm.LLMRouter {
	var channels[]model.Channel

	// 题目 1：
	// 使用 model.DB 查询所有 Status 为 1 的渠道，按 Weight 降序排列 (权重高的排前面)
	// 并将结果存入 channels 切片中。
	// 提示：使用 .Where(...).Order(...).Find(...)
	err := model.DB.Where("status = ?", 1).Order("weight DESC").Find(&channels).Error
	if err != nil {
		log.Fatalf("❌ 加载渠道配置失败: %v", err)
	}

	if len(channels) == 0 {
		log.Println("⚠️ 警告：数据库中没有任何可用的渠道配置！")
	}

	// 准备一个切片，用来存放所有的 Provider
	var providers[]llm.Provider

	// 题目 2：
	// 遍历 channels 切片。
	// 对于每一个 channel，调用 llm.NewBaseClient() 创建一个客户端实例。
	// 注意：NewBaseClient 需要 4 个参数：Name, BaseURL, Key, Model。
	// 我们的 Channel 表里有 Name, BaseURL, Key，至于 Model，你可以先传入 channel.Models (假设里面只配了一个模型名，后续我们会做更复杂的模型映射)。
	// 将创建好的实例 append 到 providers 切片中。
	for _, ch := range channels {
		client := llm.NewBaseClient(ch.Name, ch.BaseURL, ch.Key, ch.Models)
		providers = append(providers, client)
		log.Printf("🔌 成功加载渠道:[%s] %s", ch.Type, ch.Name)
	}

	// 题目 3：
	// 调用 llm.NewLLMRouter()，将 providers 切片作为可变参数传入。
	// 提示：Go 语言中，将切片作为可变参数传入需要在切片名后加 ...
	return llm.NewLLMRouter(providers...)
}

// ReloadChannelsHandler 触发渠道热加载
func ReloadChannelsHandler(routerManager *llm.RouterManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 题目 2：
		// 1. 调用 BuildDynamicRouter() 生成一个新的 router 对象。
		// 2. 调用 routerManager.Reload() 方法，把新 router 传进去完成替换。
		// 3. 返回 200 OK，提示 {"message": "渠道配置热加载成功"}
		router := BuildDynamicRouter()
		routerManager.Reload(router);
		c.JSON(http.StatusOK,gin.H{"message": "渠道配置热加载成功"})
	}
}