package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

// 密码找回：邮箱验证 + 一次性重置链接。
// - 令牌 32 字节 crypto/rand，SHA-256 落库（明文只在邮件链接里出现，不落库）
// - 30 分钟有效、一次性（条件 UPDATE 原子置位 used_at，并发双用只有一个成功）
// - 邮箱必须已绑定账号才发信：未绑定直接报错，不建令牌、不消耗邮件服务
// - 同一邮箱 60 秒内只发一封（与注册验证码重发窗口同口径）

const (
	resetTokenTTL     = 30 * time.Minute
	resetResendWindow = 60 * time.Second
)

// ResetLinkPath 前端重置页路由（#/ 哈希路由）
const ResetLinkPath = "/reset-password"

// RequestPasswordReset 校验邮箱并发出重置链接邮件。
// 返回 (devLink, retryAfter, err)：SMTP 未配置（开发模式）时 devLink 带完整
// 重置链接便于本地联调；retryAfter>0 表示触发 60 秒重发窗口。
// 邮箱未绑定账号：直接报错，不产生令牌、不发信。
func RequestPasswordReset(db *gorm.DB, email string) (devLink string, retryAfter int, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !ValidEmail(email) {
		return "", 0, fmt.Errorf("邮箱格式不正确")
	}

	// 找绑定该邮箱的账号（users.email 无唯一约束，取最早注册的一个）
	var u struct {
		ID   int64
		Name string
	}
	if err := db.Raw(`SELECT id, COALESCE(NULLIF(display_name, ''), username) AS name
		FROM users WHERE email = ? ORDER BY id LIMIT 1`, email).Scan(&u).Error; err != nil || u.ID == 0 {
		// 未绑定：直接提示（产品要求明确反馈，不消耗邮件服务）
		slog.Info("密码重置请求：邮箱未绑定账号（已拒绝）", "email", email)
		return "", 0, fmt.Errorf("该邮箱未绑定任何账号，请确认填写无误，或联系管理员重置")
	}

	// 60 秒重发窗口（同一邮箱最近一封发出未满窗口即拒绝）
	var last struct {
		CreatedAt int64 `gorm:"column:created_at"`
	}
	_ = db.Raw(`SELECT created_at FROM password_reset_tokens WHERE email = ?
		ORDER BY id DESC LIMIT 1`, email).Scan(&last).Error
	now := time.Now()
	if last.CreatedAt > 0 && now.Unix()-last.CreatedAt < int64(resetResendWindow.Seconds()) {
		left := int(int64(resetResendWindow.Seconds())-(now.Unix()-last.CreatedAt)) + 1
		return "", left, fmt.Errorf("发送太频繁，请 %d 秒后再试", left)
	}

	// 生成令牌：32 字节随机 → base64url（链接安全字符）；SHA-256 落库
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, fmt.Errorf("生成令牌失败: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])

	// 该账号的历史未用令牌一并作废（以最新邮件为准，防止旧链接残留可用）
	if err := db.Exec(`UPDATE password_reset_tokens SET used_at = ? WHERE user_id = ? AND used_at = 0`,
		now.Unix(), u.ID).Error; err != nil {
		return "", 0, fmt.Errorf("令牌写入失败: %v", err)
	}
	if err := db.Create(&model.PasswordResetToken{
		UserID: u.ID, Email: email, TokenHash: tokenHash,
		ExpireAt: now.Add(resetTokenTTL).Unix(), CreatedAt: now.Unix(),
	}).Error; err != nil {
		if database.IsDuplicateKey(err) {
			return "", 0, fmt.Errorf("发送太频繁，请稍后再试")
		}
		return "", 0, fmt.Errorf("令牌写入失败: %v", err)
	}

	link := ""
	if siteBaseURL != "" {
		link = fmt.Sprintf("%s/#%s?token=%s", strings.TrimRight(siteBaseURL, "/"), ResetLinkPath, token)
	}

	// 开发模式（SMTP 未配置）：不发信，链接写日志并返回给前端便于本地联调
	if !MailerConfigured() {
		if link == "" {
			link = fmt.Sprintf("(请配置 server.site_url) #%s?token=%s", ResetLinkPath, token)
		}
		slog.Warn("密码重置链接以开发模式返回（SMTP 未配置）",
			"email", email, "user_id", u.ID, "link", link)
		return link, 0, nil
	}
	// SMTP 已配置但 site_url 未配：无法拼出可用链接，也绝不允许把明文 token 经 HTTP 返回给前端
	if link == "" {
		return "", 0, fmt.Errorf("服务器未配置 server.site_url，无法发送重置链接，请联系管理员")
	}

	subject := BrandName + "密码重置"
	body := fmt.Sprintf(`%s，你好：

你（或他人）请求重置 %s 账号的密码。点击下方链接设置新密码（30 分钟内有效、仅可使用一次）：

%s

若非本人操作，请忽略本邮件——你的密码不会被更改。
—— %s`, u.Name, BrandName, link, BrandName)
	if err := sendMail(mailerCfg, email, subject, body); err != nil {
		// 邮件失败作废令牌：拿着已生成但没送达的链接没有意义，且避免占用重发窗口
		_ = db.Exec("DELETE FROM password_reset_tokens WHERE token_hash = ?", tokenHash).Error
		return "", 0, fmt.Errorf("邮件发送失败: %v", err)
	}
	slog.Info("密码重置邮件已发送", "email", email, "user_id", u.ID)
	return "", 0, nil
}

// ResetPassword 用邮件链接里的令牌设置新密码（一次性）。
// 错误文案统一为「链接无效或已失效」，不区分令牌不存在/过期/已用（不泄漏状态）
func ResetPassword(db *gorm.DB, token, newPassword string) error {
	if len(newPassword) < 6 {
		return fmt.Errorf("新密码至少 6 位")
	}
	sum := sha256.Sum256([]byte(token))
	var row model.PasswordResetToken
	if err := db.Where("token_hash = ?", hex.EncodeToString(sum[:])).First(&row).Error; err != nil {
		return fmt.Errorf("重置链接无效或已失效，请重新获取")
	}
	if time.Now().Unix() > row.ExpireAt || row.UsedAt != 0 {
		return fmt.Errorf("重置链接无效或已失效，请重新获取")
	}
	// 原子认领令牌：并发双用（同一链接两处同时提交）只有一个能置位 used_at
	res := db.Exec("UPDATE password_reset_tokens SET used_at = ? WHERE id = ? AND used_at = 0",
		time.Now().Unix(), row.ID)
	if res.Error != nil || res.RowsAffected == 0 {
		return fmt.Errorf("重置链接无效或已失效，请重新获取")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("密码加密失败")
	}
	if err := db.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), row.UserID).Error; err != nil {
		return fmt.Errorf("重置失败，请重试")
	}
	slog.Info("密码已通过邮件链接重置", "user_id", row.UserID, "email", row.Email)
	return nil
}
