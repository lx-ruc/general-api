package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
)

const (
	ctxUID   = "uid"
	ctxRole  = "role"
	ctxOrgID = "jwt_org_id"
)

// JWTAuth 解析管理台 Bearer token，并回库校验账号状态与角色
// （JWT 本身无状态，不回库则禁用/删除账号后存量 token 在 TTL 内仍有效，违反"状态即时生效"）
func JWTAuth(secret string, db *gorm.DB) gin.HandlerFunc {
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
		var row struct {
			Status   int
			Role     string
			OrgState *int // LEFT JOIN：系统管理员无组织为 NULL
		}
		if err := db.Raw(`
			SELECT u.status, u.role, o.status AS org_state
			FROM users u LEFT JOIN orgs o ON o.id = u.org_id
			WHERE u.id = ?`, claims.UID).Scan(&row).Error; err != nil || row.Role == "" {
			abortUnauthorized(c, "account not found or deleted")
			return
		}
		if row.Status != 1 {
			abortUnauthorized(c, "account is disabled")
			return
		}
		if row.OrgState != nil && *row.OrgState == 0 {
			// 组织被手动停用：存量 token 即时失效（欠费停服 2 不拦管理台，数据面另行拦截）
			abortUnauthorized(c, "organization is disabled")
			return
		}
		c.Set(ctxUID, claims.UID)
		c.Set(ctxRole, row.Role) // 以库内角色为准，角色变更即时生效
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

// GetOrgID 当前登录用户所属组织；系统管理员返回 nil
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
