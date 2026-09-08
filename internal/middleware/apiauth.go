package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
)

const ctxKeyInfo = "key_info"

// KeyInfo /v1 数据面鉴权结果：一次联表带出 key/user/org 身份与状态
type KeyInfo struct {
	KeyID        int64  `json:"key_id"`
	KeyPrefix    string `json:"key_prefix"`
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	UserRole     string `json:"user_role"`
	OrgID        int64  `json:"org_id"`
	OrgName      string `json:"org_name"`
	CostCenterID *int64 `json:"cost_center_id"` // key 归集中心（结算时快照进 usage_logs）
	UserLimit    *int64 `json:"user_limit"`
	UserUsed     int64  `json:"user_used"`
	OrgLimit     int64  `json:"org_limit"`
	OrgUsed      int64  `json:"org_used"`

	// Playground 管理台「在线体验」注入的合成身份标记：模型授权已由管理面按角色校验
	// （见 api/playground），此处跳过子账号白名单检查；系统管理员（OrgID=0）无额度语义，
	// 结算时不计费。数据面 API key 鉴权永远不设此标记。
	Playground bool `json:"playground,omitempty"`
}

// SetKeyInfo 管理面「在线体验」注入合成身份后复用数据面编排（身份由 JWT 保证，不走 API key）
func SetKeyInfo(c *gin.Context, ki *KeyInfo) {
	c.Set(ctxKeyInfo, ki)
}

// APIKeyAuth /v1 数据面鉴权：sk- key → SHA-256 → 唯一索引等值查找（单查询联表带出全部状态）
func APIKeyAuth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := bearerToken(c)
		if !strings.HasPrefix(key, "sk-") {
			openaiAbort(c, http.StatusUnauthorized, "invalid_api_key", "invalid API key format, expected Bearer sk-...")
			return
		}
		var row struct {
			KeyInfo
			ExpiredAt  *int64
			UserStatus int
			OrgStatus  int
		}
		err := db.Raw(`
			SELECT k.id AS key_id, k.key_prefix, k.expired_at, k.cost_center_id,
			       u.id AS user_id, u.username, u.role AS user_role, u.status AS user_status,
			       u.quota_limit AS user_limit, u.quota_used AS user_used,
			       o.id AS org_id, o.name AS org_name, o.status AS org_status,
			       o.quota_limit AS org_limit, o.quota_used AS org_used
			FROM api_keys k
			JOIN users u ON u.id = k.user_id
			JOIN orgs o  ON o.id = k.org_id
			WHERE k.key_hash = ? AND k.status = 1`, auth.HashAPIKey(key)).
			Scan(&row).Error
		if err != nil || row.KeyID == 0 {
			openaiAbort(c, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
			return
		}
		if row.ExpiredAt != nil && *row.ExpiredAt <= time.Now().Unix() {
			openaiAbort(c, http.StatusUnauthorized, "invalid_api_key", "API key has expired")
			return
		}
		if row.UserStatus != 1 || row.OrgStatus == 0 {
			openaiAbort(c, http.StatusForbidden, "permission_error", "account or organization is disabled")
			return
		}
		if row.OrgStatus == 2 { // 欠费停服：额度耗尽自动置位，充值后自动恢复
			openaiAbort(c, http.StatusForbidden, "insufficient_balance",
				"organization suspended for arrears (quota exhausted), please contact the platform admin to recharge")
			return
		}
		ki := row.KeyInfo
		c.Set(ctxKeyInfo, &ki)
		// 异步更新 last_used_at（单连接串行化下由 busy_timeout 兜底，不阻塞请求）
		go func() {
			_ = db.Exec("UPDATE api_keys SET last_used_at = ? WHERE id = ?", time.Now().Unix(), ki.KeyID).Error
		}()
		c.Next()
	}
}

func GetKeyInfo(c *gin.Context) *KeyInfo {
	if v, ok := c.Get(ctxKeyInfo); ok {
		if ki, ok := v.(*KeyInfo); ok {
			return ki
		}
	}
	return nil
}

func openaiAbort(c *gin.Context, status int, errType, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{"message": msg, "type": errType, "code": errType},
	})
}
