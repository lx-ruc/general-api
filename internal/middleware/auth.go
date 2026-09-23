package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
)

const (
	ctxUID   = "uid"
	ctxRole  = "role"
	ctxOrgID = "jwt_org_id"
)

// JWTAuth 管理面鉴权（双轨）：
//   - 网页登录 JWT：无状态解析 + 回库校验账号状态与角色
//     （不回库则禁用/删除账号后存量 token 在 TTL 内仍有效，违反"状态即时生效"）
//   - 访问令牌 tgp_ 前缀：SHA-256 查表载入属主 → 走完全相同的 RBAC / org 隔离链路，
//     吊销（status=0）/过期即时 401；权限 = 属主用户权限
func JWTAuth(secret string, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := bearerToken(c)
		if tokenStr == "" {
			abortUnauthorized(c, "missing authorization token")
			return
		}
		var uid int64
		var jwtOrgID *int64
		var jwtVer string
		if strings.HasPrefix(tokenStr, "tgp_") {
			u, err := authByAccessToken(db, tokenStr)
			if err != nil {
				abortUnauthorized(c, err.Error())
				return
			}
			uid = u
		} else {
			claims, err := auth.ParseToken(secret, tokenStr)
			if err != nil {
				abortUnauthorized(c, "invalid or expired token")
				return
			}
			uid, jwtOrgID, jwtVer = claims.UID, claims.OrgID, claims.Ver
		}
		var row struct {
			Status       int
			Role         string
			OrgID        *int64
			PasswordHash string
			OrgState     *int // LEFT JOIN：系统管理员无组织为 NULL
		}
		if err := db.Raw(`
			SELECT u.status, u.role, u.org_id, u.password_hash, o.status AS org_state
			FROM users u LEFT JOIN orgs o ON o.id = u.org_id
			WHERE u.id = ?`, uid).Scan(&row).Error; err != nil || row.Role == "" {
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
		// 会话指纹（仅 JWT 轨）：改密码/重置密码后旧登录会话即时失效；
		// 空 ver = 升级前签发的存量 token，放行到自然过期。tgp_ 令牌不受密码变更影响（长期凭据，吊销走管理台）
		if jwtVer != "" && auth.SessionVer(row.PasswordHash) != jwtVer {
			abortUnauthorized(c, "session expired, please login again")
			return
		}
		c.Set(ctxUID, uid)
		c.Set(ctxRole, row.Role) // 以库内角色为准，角色变更即时生效
		// org 归属以库内为准（账号调动/迁移即时生效）；库内无归属时才回退签发时值
		if row.OrgID != nil && *row.OrgID != 0 {
			c.Set(ctxOrgID, row.OrgID)
		} else if jwtOrgID != nil {
			c.Set(ctxOrgID, jwtOrgID)
		}
		c.Next()
	}
}

// authByAccessToken 访问令牌查表：SHA-256 命中、未吊销、未过期 → 属主 uid；
// last_used_at 节流更新（>60s 才写，避免高频调用写放大）
func authByAccessToken(db *gorm.DB, token string) (int64, error) {
	var row struct {
		ID        int64
		UserID    int64
		ExpiresAt int64
	}
	if err := db.Raw(`SELECT id, user_id, expires_at FROM access_tokens
		WHERE token_hash = ? AND status = 1`, auth.HashAPIKey(token)).Scan(&row).Error; err != nil || row.ID == 0 {
		return 0, errors.New("invalid access token")
	}
	if row.ExpiresAt > 0 && row.ExpiresAt < time.Now().Unix() {
		return 0, errors.New("access token expired")
	}
	go db.Exec(`UPDATE access_tokens SET last_used_at = ?
		WHERE id = ? AND (last_used_at = 0 OR last_used_at < ?)`,
		time.Now().Unix(), row.ID, time.Now().Unix()-60)
	return row.UserID, nil
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	// Anthropic 系客户端（Claude Code 等）原生用 x-api-key 头携带密钥
	if k := c.GetHeader("x-api-key"); k != "" {
		return strings.TrimSpace(k)
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
