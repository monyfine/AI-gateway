// internal/api/user.go
package api

import (
	"ai-gateway/internal/model"
	"ai-gateway/pkg/utils"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type UserRegisterReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// UserRegisterHandler 普通用户自助注册
func UserRegisterHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req UserRegisterReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest,gin.H{"error":"请求参数格式错误或缺失必填字段"})
			return
		}
		var user model.TenantUser
		if err := model.DB.Where("username = ?",req.Username).First(&user).Error; err != nil{
			// 判断是否是“找不到记录”
			if errors.Is(err, gorm.ErrRecordNotFound) {
				hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
					return
				}
				newUser := model.TenantUser{
					Username: req.Username,
					Password: string(hashedPassword),
				}

				if err := model.DB.Create(&newUser).Error; err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "用户创建失败"})
					return
				}

				// 5. 返回 201 Created
				c.JSON(http.StatusCreated, gin.H{
					"code": 201,
					"msg":  "注册成功",
					"data": gin.H{
						"user_id":  newUser.ID,
						"username": newUser.Username,
					},
				})
				return
			}
			// 其他数据库层面的严重错误
			c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库内部错误"})
			return
		}
		c.JSON(http.StatusBadRequest,gin.H{"error":"用户已存在"})
	}
}

// UserLoginHandler 普通用户登录
func UserLoginHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
        // 请手写完整的登录逻辑 (参数绑定 -> 查库 -> bcrypt比对 -> GenerateToken -> 返回 Token)
		var req UserRegisterReq
		if err := c.ShouldBindJSON(&req); err != nil{
			c.JSON(http.StatusBadRequest,gin.H{"error":"请求参数格式错误或缺失必填字段"})
			return
		}
		var user model.TenantUser
		if err := model.DB.Where("username = ?",req.Username).First(&user).Error; err != nil{
			// 判断是否是“找不到记录”的特定错误
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound,gin.H{"error":"用户不存在"})
				return
			}
			// 其他数据库层面的严重错误
			c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库内部错误"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
			return
		}
		token, err := utils.GenerateToken(req.Username, "user", user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "系统生成令牌失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "登录成功",
			"token":   token,
		})
	}
}