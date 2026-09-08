package middleware

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/model"
)

var pwdRe = regexp.MustCompile(`("(?:password|new_password|old_password|upstream_key)"\s*:\s*")[^"]*(")`)

// Audit 管理台写操作审计：记录 /api 下所有非 GET 请求（登录除外）。
// 密码/密钥字段脱敏；数据面 /v1 不记（量大且已有 usage_logs）。
func Audit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead ||
			!strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.Next()
			return
		}
		// 登录请求不落库（成功登录有 last_login_at，失败有 IP 限流）；
		// 在线体验对话不落库（对话内容不进审计，计量已有 usage_logs 承载）
		if c.Request.URL.Path == "/api/auth/login" || c.Request.URL.Path == "/api/auth/send-code" ||
			c.Request.URL.Path == "/api/playground/chat" {
			c.Next()
			return
		}
		var body []byte
		if c.Request.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(c.Request.Body, 4<<10))
			c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		c.Next()
		// 异步落库，不阻塞响应
		uid := GetUID(c)
		detail := pwdRe.ReplaceAllString(string(body), "${1}***${2}")
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		go func() {
			_ = db.Create(&model.AuditLog{
				ActorID:   nilIfZero(uid),
				Actor:     GetRole(c), // 角色；具体用户见 actor_id
				Method:    c.Request.Method,
				Path:      c.Request.URL.Path,
				Status:    c.Writer.Status(),
				Detail:    detail,
				IP:        c.ClientIP(),
				CreatedAt: time.Now().Unix(),
			}).Error
		}()
	}
}

func nilIfZero(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
