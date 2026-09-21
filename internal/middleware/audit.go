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

// 任意以 password 结尾的字段（password/admin_password/new_password/old_password…）、
// upstream_key 与 keys 数组（渠道 Key 池批量入参）都脱敏——值形态度不限于字符串
var pwdRe = regexp.MustCompile(`("(?:[a-z_]*password|upstream_key|keys)"\s*:\s*)(?:"[^"]*"|\[[^\]]*\])`)

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
		detail := pwdRe.ReplaceAllString(string(body), `${1}"***"`)
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

// ScrubAuditHistory 启动时补洗存量审计明细：旧版本落库未脱敏的密码/上游 Key
// 用同一规则补脱敏（幂等——已脱敏的 "***" 再洗不变），返回补洗行数。
// 审计只记管理面写操作，量级小，全量扫一遍在启动期可接受。
func ScrubAuditHistory(db *gorm.DB) int64 {
	type row struct {
		ID     int64  `gorm:"column:id"`
		Detail string `gorm:"column:detail"`
	}
	var rows []row
	if err := db.Raw(`SELECT id, detail FROM audit_logs WHERE detail != ''`).Scan(&rows).Error; err != nil {
		return 0
	}
	var n int64
	for _, r := range rows {
		masked := pwdRe.ReplaceAllString(r.Detail, `${1}"***"`)
		if masked == r.Detail {
			continue
		}
		if err := db.Exec("UPDATE audit_logs SET detail = ? WHERE id = ?", masked, r.ID).Error; err == nil {
			n++
		}
	}
	return n
}
