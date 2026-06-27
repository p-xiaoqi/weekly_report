package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

func InitJWT(secret string) {
	jwtSecret = []byte(secret)
}

// Claims JWT 自定义声明
type Claims struct {
	UserID       string `json:"user_id"`
	FeishuOpenID string `json:"feishu_open_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	jwt.RegisteredClaims
}

// GenerateToken 签发 JWT
func GenerateToken(userID, feishuOpenID, name, role string, expireHours int) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:       userID,
		FeishuOpenID: feishuOpenID,
		Name:         name,
		Role:         role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(expireHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "weekly-report-system",
			Subject:   userID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ParseToken 解析 JWT
func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, fmt.Errorf("invalid token")
}

// JWTAuth JWT 认证中间件
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 优先从 Authorization 头获取
		authHeader := c.GetHeader("Authorization")
		var tokenString string
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// 2. 如果 Header 没有，尝试从 Cookie 获取（兼容现有测试页面）
		if tokenString == "" {
			if cookie, err := c.Cookie("jwt_token"); err == nil && cookie != "" {
				tokenString = cookie
			}
		}

		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录，请先授权"})
			return
		}

		claims, err := ParseToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "登录已过期，请重新授权"})
			return
		}

		// 将用户信息写入上下文
		c.Set("user_id", claims.UserID)
		c.Set("feishu_open_id", claims.FeishuOpenID)
		c.Set("user_name", claims.Name)
		c.Set("user_role", claims.Role)
		c.Next()
	}
}

// JWTOrCookieAuth JWT 或 Cookie 双兼容认证中间件
func JWTOrCookieAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 优先尝试 JWT
		authHeader := c.GetHeader("Authorization")
		var tokenString string
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if tokenString == "" {
			if cookie, err := c.Cookie("jwt_token"); err == nil && cookie != "" {
				tokenString = cookie
			}
		}

		if tokenString != "" {
			claims, err := ParseToken(tokenString)
			if err == nil {
				c.Set("user_id", claims.UserID)
				c.Set("feishu_open_id", claims.FeishuOpenID)
				c.Set("user_name", claims.Name)
				c.Set("user_role", claims.Role)
				c.Next()
				return
			}
		}

		// 2. JWT 失败，回退到 Cookie（兼容现有测试页面）
		userID, err := c.Cookie("user_id")
		if err != nil || userID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录，请先授权"})
			return
		}
		c.Set("user_id", userID)
		c.Set("user_role", "member")
		c.Next()
	}
}
func OptionalJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		var tokenString string
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if tokenString == "" {
			if cookie, err := c.Cookie("jwt_token"); err == nil && cookie != "" {
				tokenString = cookie
			}
		}

		if tokenString != "" {
			claims, err := ParseToken(tokenString)
			if err == nil {
				c.Set("user_id", claims.UserID)
				c.Set("feishu_open_id", claims.FeishuOpenID)
				c.Set("user_name", claims.Name)
				c.Set("user_role", claims.Role)
			}
		}
		c.Next()
	}
}
