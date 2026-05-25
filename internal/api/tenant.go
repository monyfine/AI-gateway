package api

import (
	"ai-gateway/internal/model"
	"net/http"
	"strconv"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)
type TenantUpdateAppKeyRequest struct {
	AppName string `json:"app_name"`
	Status  int    `json:"status"` // 允许租户自己禁用 Key (比如传 0)
}
// TenantCreateAppKeyHandler 租户自己创建 API Key
func TenantCreateAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req CreateAppKeyRequest // 复用之前的结构体即可
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}

		// 题目 2.1：从 Context 中获取当前登录用户的 user_id
		userIDInterface, _ := c.Get("user_id")
		userID := userIDInterface.(uint)
		tpm := req.TPMLimit
		if tpm <= 0{
			tpm = 1000
		}
		rpm := req.RPMLimit
		if rpm <= 0{
			rpm = 60
		}
		// 题目 2.2：构造 AppKey，强行绑定 UserID，并赠送 100000 体验金
		newAppKey := model.AppKey{
			Key: "sk-"+uuid.New().String(),
			AppName: req.AppName,
			UserID: userID,
			Balance: 100000,
			TPMLimit: tpm,
			RPMLimit: rpm,
			Status:   1,
		}
		
		// 题目 2.3：保存入库并返回 201 Created
		if err := model.DB.Create(&newAppKey).Error; err != nil{
			c.JSON(http.StatusInternalServerError, gin.H{"error": "API Key 创建失败，请稍后重试"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"code": 201,
			"msg":  "创建成功，已赠送 10万 Token 体验金！",
			"data": newAppKey,
		})
	}
}

// TenantGetAppKeyListHandler 租户获取自己的 API Key 列表
func TenantGetAppKeyListHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 100 { // 限制单页最大拉取数，防止被恶意拖垮数据库
			size = 10
		}
		offset := (page - 1) * size
		// 题目 2.4：从 Context 中获取 user_id
		userIDInterface, _ := c.Get("user_id")
		userID := userIDInterface.(uint)
		var total int64
		var appKeys []model.AppKey
		// 题目 2.5：带上 user_id 过滤条件进行 GORM 查询！
		// 统计总数：model.DB.Model(&model.AppKey{}).Where("user_id = ?", userID).Count(&total)
		// 查询列表：model.DB.Where("user_id = ?", userID).Offset(offset).Limit(size).Find(&appKeys)
		if err := model.DB.Model(&model.AppKey{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "统计数据失败"})
			return
		}
		if err := model.DB.Where("user_id = ?", userID).
			Order("created_at DESC"). // 加上降序排列，新创建的在最前面
			Offset(offset).Limit(size).
			Find(&appKeys).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "拉取列表失败"})
			return
		}
		// 题目 2.6：返回 200 OK 和数据
		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"msg":  "success",
			"data": gin.H{
				"total": total,
				"page":  page,
				"size":  size,
				"list":  appKeys,
			},
		})
	}
}
// TenantUpdateAppKeyHandler 租户更新自己的 API Key
func TenantUpdateAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 从 URL 获取要更新的 Key 的 ID
		idStr := c.Param("id")
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "无效的 ID 格式"})
			return
		}

		// 2. 绑定请求体到 TenantUpdateAppKeyRequest 结构体
		var req TenantUpdateAppKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "请求参数错误", "details": err.Error()})
			return
		}

		// 3. 从 Context 中获取当前登录用户的 user_id (做类型断言)
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未获取到用户身份信息"})
			return
		}

		var userID uint
		switch v := userIDVal.(type) {
		case float64: // JWT 解析出的数字默认是 float64
			userID = uint(v)
		case uint:
			userID = v
		case int:
			userID = uint(v)
		default:
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "用户 ID 格式异常"})
			return
		}

		// 4. 查库校验：通过 id + user_id 确保只能修改自己的数据 (防止水平越权)
		var appKey model.AppKey
		if err := model.DB.Where("id = ? AND user_id = ?", id, userID).First(&appKey).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "找不到该记录或无权修改"})
			return
		}

		// 5. 构造 updateData map，只更新 app_name 和 status
		updateData := make(map[string]interface{})
		
		// 只有当 AppName 不为空时才更新它
		if req.AppName != "" {
			updateData["app_name"] = req.AppName
		}
		
		// ⚠️ 核心点：因为 0 是合法值（禁用），且 int 的默认值就是 0
		// 所以我们无法区分前端是“没传 status”还是“传了 0”
		// 这里的处理逻辑是：无脑覆盖更新 status，这就要求前端每次请求都要带上正确的 status
		updateData["status"] = req.Status

		// 6. 执行更新：使用 Map 更新，GORM 不会忽略零值 0
		if err := model.DB.Model(&appKey).Updates(updateData).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
			return
		}

		// 补全最新数据用于响应返回
		if req.AppName != "" {
			appKey.AppName = req.AppName
		}
		appKey.Status = req.Status

		// 7. 返回 200 OK 和更新后的数据
		c.JSON(http.StatusOK, gin.H{
			"message": "更新成功",
			"data":    appKey,
		})
	}
}

// TenantDeleteAppKeyHandler 租户删除自己的 API Key
func TenantDeleteAppKeyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 从 URL 获取要删除的 Key 的 ID: c.Param("id")
		idStr := c.Param("id")
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "无效的 ID 格式"})
			return
		}
		// 2. 从 Context 中获取当前登录用户的 user_id
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未获取到用户身份信息"})
			return
		}

		var userID uint
		switch v := userIDVal.(type) {
		case float64: // JWT 解析出的数字默认是 float64
			userID = uint(v)
		case uint:
			userID = v
		case int:
			userID = uint(v)
		default:
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "用户 ID 格式异常"})
			return
		}
		// 3. 执行删除：必须带上 user_id 条件！防止越权删除别人的 Key
		//    例如：model.DB.Where("id = ? AND user_id = ?", id, userID).Delete(&model.AppKey{})
		result := model.DB.Where("id = ? AND user_id = ?", id, userID).Delete(&model.AppKey{})
		
		// 检查数据库执行本身是否出错
		if result.Error != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "删除失败，服务器内部错误"})
			return
		}

		// 检查是否真的有数据被删除 (防御水平越权或重复删除)
		if result.RowsAffected == 0 {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "找不到该记录或无权删除"})
			return
		}
		// 4. 返回 204 No Content
		c.Status(http.StatusNoContent)
	}
}