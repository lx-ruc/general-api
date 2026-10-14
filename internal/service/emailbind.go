package service

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

// 邮箱绑定/换绑：登录态下向目标邮箱发验证码，验证通过写入 users.email。
// 绑定后该账号即可走邮件自助找回密码（reset.go 不分角色，按 users.email 查号）。
// - 验证码 user 维度（同一邮箱可被多个账号同时发起绑定，各验各的），每次发码作废旧码
// - 5 分钟有效、60 秒重发窗、错 5 次作废，与注册验证码同口径
// - 查重拒绝已占用邮箱（users.email 唯一部分索引为并发兜底，应用层查重保证友好文案）

const (
	bindCodeTTL      = 5 * time.Minute
	bindResendWindow = 60 * time.Second
	bindMaxAttempts  = 5
)

// userNameOf 取账号展示名（邮件称呼用；无展示名回退用户名）
func userNameOf(db *gorm.DB, userID int64) (string, error) {
	var name string
	if err := db.Raw(`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = ?`,
		userID).Scan(&name).Error; err != nil || name == "" {
		return "", fmt.Errorf("账号不存在")
	}
	return name, nil
}

// emailOwner 查占用某邮箱的账号 id（0=未占用）
func emailOwner(db *gorm.DB, email string) int64 {
	var id int64
	_ = db.Raw(`SELECT id FROM users WHERE email != '' AND email = ? LIMIT 1`, email).Scan(&id).Error
	return id
}

// SendBindCode 给登录用户的目标邮箱发绑定验证码。
// 返回 (devCode, retryAfter, err)：SMTP 未配置（开发模式）时 devCode 带明文验证码；
// retryAfter>0 表示触发 60 秒重发窗口。查重三态：他人占用 / 已是本人 / 可用。
func SendBindCode(db *gorm.DB, userID int64, email string) (devCode string, retryAfter int, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !ValidEmail(email) {
		return "", 0, fmt.Errorf("邮箱格式不正确")
	}
	if owner := emailOwner(db, email); owner != 0 {
		if owner == userID {
			return "", 0, fmt.Errorf("该邮箱已是当前账号的绑定邮箱")
		}
		return "", 0, fmt.Errorf("该邮箱已被其他账号绑定")
	}

	// 60 秒重发窗口（user 维度，最近一封发出未满窗口即拒绝）
	var last struct {
		SentAt int64 `gorm:"column:sent_at"`
	}
	_ = db.Raw(`SELECT sent_at FROM email_bind_codes WHERE user_id = ?
		ORDER BY id DESC LIMIT 1`, userID).Scan(&last).Error
	now := time.Now()
	if last.SentAt > 0 && now.Unix()-last.SentAt < int64(bindResendWindow.Seconds()) {
		left := int(int64(bindResendWindow.Seconds())-(now.Unix()-last.SentAt)) + 1
		return "", left, fmt.Errorf("发送太频繁，请 %d 秒后再试", left)
	}

	name, err := userNameOf(db, userID)
	if err != nil {
		return "", 0, err
	}

	// 6 位数字验证码（与注册验证码同款生成）
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", 0, fmt.Errorf("生成验证码失败: %v", err)
	}
	code := fmt.Sprintf("%06d", n.Int64())
	// 顺带清理过期验证码，再作废该用户旧码（以最新邮件为准）
	_ = db.Exec("DELETE FROM email_bind_codes WHERE expire_at < ?", now.Unix()).Error
	if err := db.Exec("DELETE FROM email_bind_codes WHERE user_id = ?", userID).Error; err != nil {
		return "", 0, fmt.Errorf("验证码存储失败: %v", err)
	}
	if err := db.Create(&model.EmailBindCode{
		UserID: userID, Email: email, Code: code,
		ExpireAt: now.Add(bindCodeTTL).Unix(), SentAt: now.Unix(), CreatedAt: now.Unix(),
	}).Error; err != nil {
		return "", 0, fmt.Errorf("验证码存储失败: %v", err)
	}

	// 开发模式（SMTP 未配置）：不发信，验证码写日志并返回给前端便于本地联调
	if !MailerConfigured() {
		slog.Warn("邮箱绑定验证码以开发模式返回（SMTP 未配置）",
			"user_id", userID, "email", email, "code", code)
		return code, 0, nil
	}
	if err := sendMail(mailerCfg, email, BrandName+"邮箱绑定验证码",
		fmt.Sprintf("%s，你好：\n\n你正在为账号绑定本邮箱，验证码：%s\n\n5 分钟内有效。若非本人操作，请忽略本邮件——你的账号不会发生变化。\n—— %s",
			name, code, BrandName)); err != nil {
		// 发送失败删行：验证码没送达没有意义，也不占用重发窗口
		_ = db.Exec("DELETE FROM email_bind_codes WHERE user_id = ?", userID).Error
		return "", 0, fmt.Errorf("邮件发送失败: %v", err)
	}
	slog.Info("邮箱绑定验证码已发送", "user_id", userID, "email", email)
	return "", 0, nil
}

// BindEmail 校验并消费验证码，落库 users.email；成功后作废该账号历史未用重置令牌
// （防旧邮箱的 30 分钟重置链接跨换绑存续），并发「已绑定」通知邮件与站内信。
func BindEmail(db *gorm.DB, userID int64, email, code string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !ValidEmail(email) {
		return fmt.Errorf("邮箱格式不正确")
	}
	// 验证锚定该用户最新一行验证码
	var row model.EmailBindCode
	if err := db.Raw(`SELECT * FROM email_bind_codes WHERE user_id = ?
		ORDER BY id DESC LIMIT 1`, userID).Scan(&row).Error; err != nil || row.ID == 0 {
		return fmt.Errorf("请先获取验证码")
	}
	if time.Now().Unix() > row.ExpireAt {
		_ = db.Exec("DELETE FROM email_bind_codes WHERE id = ?", row.ID).Error
		return fmt.Errorf("验证码已过期，请重新获取")
	}
	// 邮箱一致性：验证码是发给 row.Email 的，不许拿 A 邮箱的码绑 B 邮箱
	if row.Email != email {
		return fmt.Errorf("邮箱与验证码不匹配，请重新获取")
	}
	if row.Code != code {
		// 原子自增 + 上限封口（与注册验证码同款：并发错码不丢计数）
		res := db.Exec(`UPDATE email_bind_codes SET attempts = attempts + 1
			WHERE id = ? AND attempts < ?`, row.ID, bindMaxAttempts)
		if res.Error == nil && res.RowsAffected > 0 {
			var attempts int
			_ = db.Raw("SELECT attempts FROM email_bind_codes WHERE id = ?", row.ID).Scan(&attempts).Error
			if attempts >= bindMaxAttempts {
				_ = db.Exec("DELETE FROM email_bind_codes WHERE id = ?", row.ID).Error
				return fmt.Errorf("错误次数过多，验证码已作废，请重新获取")
			}
		}
		return fmt.Errorf("验证码不正确")
	}
	// 原子消费：删得到行才算验证成功（并发同码请求只有一个能删到）
	res := db.Exec("DELETE FROM email_bind_codes WHERE id = ? AND code = ?", row.ID, row.Code)
	if res.Error != nil || res.RowsAffected == 0 {
		return fmt.Errorf("验证码不正确")
	}

	// 消费后再查重：发码与确认之间邮箱可能被他人抢占
	if owner := emailOwner(db, email); owner != 0 && owner != userID {
		return fmt.Errorf("该邮箱已被其他账号绑定，请重新获取验证码")
	}
	name, err := userNameOf(db, userID)
	if err != nil {
		return err
	}
	if err := db.Exec("UPDATE users SET email = ?, updated_at = ? WHERE id = ?",
		email, time.Now().Unix(), userID).Error; err != nil {
		// 并发兜底：应用层查重的快照窗口内两人同时落库，后来者撞唯一部分索引
		if database.IsDuplicateKey(err) {
			return fmt.Errorf("该邮箱已被其他账号绑定")
		}
		return fmt.Errorf("绑定失败，请重试")
	}

	// 作废该账号历史未用重置令牌：旧邮箱里未过期的重置链接不得跨换绑存续
	_ = db.Exec("UPDATE password_reset_tokens SET used_at = ? WHERE user_id = ? AND used_at = 0",
		time.Now().Unix(), userID).Error

	// 「已绑定」告知邮件：新邮箱主人收到落位确认（失败不影响绑定结果）
	if MailerConfigured() {
		if err := sendMail(mailerCfg, email, BrandName+"邮箱绑定成功",
			fmt.Sprintf("%s，你好：\n\n你的 %s 账号已绑定本邮箱。此后可在登录页通过「忘记密码」用本邮箱自助重置密码。\n若非本人操作，请立即登录修改密码并联系管理员。\n—— %s",
				name, BrandName, BrandName)); err != nil {
			slog.Warn("邮箱绑定成功通知邮件发送失败", "user_id", userID, "email", email, "err", err)
		}
	}
	// 站内信留下不可删除的安全痕迹（会话被盗后换绑邮箱有据可查）
	NotifyUserInsite(db, userID, NotifyTypeEmailBind, "邮箱绑定成功",
		fmt.Sprintf("账号已绑定邮箱 %s，可用于自助找回密码。若非本人操作，请立即修改密码并联系管理员。", email))
	slog.Info("邮箱绑定成功", "user_id", userID, "email", email)
	return nil
}
