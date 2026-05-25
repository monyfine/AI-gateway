package utils

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ⚠️ 生产环境中，这个秘钥应该放在环境变量里
var jwtSecret =[]byte("ai-gateway-super-secret-key")

// CustomClaims 自定义 JWT 的载荷（Payload）
type CustomClaims struct {
	Username string `json:"username"`
	Role     string `json:"role"`    //"admin" 或 "user"
	UserID   uint   `json:"user_id,omitempty"` //如果是 user，存入他的真实 ID
	//如果你不加 omitempty，当生成管理员的 Token 时，JWT 的 Payload 解析出来会是这样的
	jwt.RegisteredClaims // 包含官方标准的字段，如过期时间(exp)
}

// GenerateToken 生成 JWT 令牌
func GenerateToken(username string, role string, userID uint) (string, error) {
	// 1. 构造 Payload
	claims := CustomClaims{
		Username: username,
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)), // 设置 24 小时过期
			IssuedAt:  jwt.NewNumericDate(time.Now()),                     // 签发时间
			Issuer:    "ai-gateway-admin",                                 // 签发人
		},
	}
	if role == "user"{
		claims.UserID=userID
	}
	// 2. 选择签名算法 (HS256 是一种对称加密算法)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// 3. 使用秘钥进行签名，生成最终的字符串
	return token.SignedString(jwtSecret)
}

// ParseToken 解析并校验 JWT 令牌
func ParseToken(tokenString string) (*CustomClaims, error) {
	// 解析 Token，并提供秘钥用于校验签名
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	// 校验 Token 是否有效，并提取我们自定义的 Claims
	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}