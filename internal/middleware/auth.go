package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/auth"
)

const (
	ctxUID   = "uid"
	ctxRole  = "role"
	ctxOrgID = "jwt_org_id"
)

// JWTAuth 解析管理台 Bearer token，注入 uid/role/org_id
func JWTAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := bearerToken(c)
		if tokenStr == "" {
			abortUnauthorized(c, "missing authorization token")
			return
		}
		claims, err := auth.ParseToken(secret, tokenStr)
		if err != nil {
			abortUnauthorized(c, "invalid or expired token")
			return
		}
		c.Set(ctxUID, claims.UID)
		c.Set(ctxRole, claims.Role)
		c.Set(ctxOrgID, claims.OrgID)
		c.Next()
	}
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func abortUnauthorized(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error": gin.H{"message": msg, "type": "unauthorized"},
	})
}

func GetUID(c *gin.Context) int64  { return getContextInt64(c, ctxUID) }
func GetRole(c *gin.Context) string {
	if v, ok := c.Get(ctxRole); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetOrgID 当前登录用户所属组织；平台管理员返回 nil
func GetOrgID(c *gin.Context) *int64 {
	if v, ok := c.Get(ctxOrgID); ok {
		if p, ok := v.(*int64); ok {
			return p
		}
	}
	return nil
}

func getContextInt64(c *gin.Context, key string) int64 {
	if v, ok := c.Get(key); ok {
		if n, ok := v.(int64); ok {
			return n
		}
	}
	return 0
}
