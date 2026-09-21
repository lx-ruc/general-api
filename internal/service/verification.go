package service

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/big"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/model"
)

// 邮箱验证码：存数据库（多实例共享；5 分钟有效，60 秒重发间隔，验证错 5 次作废）
const (
	codeTTL      = 5 * time.Minute
	resendWindow = 60 * time.Second
	maxAttempts  = 5
)

type Verification struct {
	db   *gorm.DB
	smtp *config.Smtp
}

func NewVerification(db *gorm.DB, smtp *config.Smtp) *Verification {
	return &Verification{db: db, smtp: smtp}
}

// emailRe 收敛为白名单（HTML5 规范邮箱正则）：local 部分仅限可打印 ASCII 标点，
// 域名部分仅限字母数字与连字符。邮箱会拼进 SMTP 头（To:），必须整体拒绝
// 控制字符与空白，防止 CRLF 邮件头注入（如 "a@b.com\r\nBcc:..."）。
var emailRe = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// ValidEmail 简易邮箱格式校验
func ValidEmail(s string) bool {
	return len(s) <= 254 && strings.Contains(s, ".") && emailRe.MatchString(s)
}

type verifyRow struct {
	Email    string `gorm:"column:email"`
	Code     string `gorm:"column:code"`
	ExpireAt int64  `gorm:"column:expire_at"`
	SentAt   int64  `gorm:"column:sent_at"`
	Attempts int    `gorm:"column:attempts"`
}

// SendCode 生成并向邮箱发送验证码；返回 (devCode, retryAfter, err)。
// devCode 非空表示 SMTP 未配置（开发模式），验证码直接返回给调用方。
func (v *Verification) SendCode(email string) (devCode string, retryAfter int, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !ValidEmail(email) {
		return "", 0, fmt.Errorf("邮箱格式不正确")
	}
	now := time.Now().Unix()
	var prev verifyRow
	_ = v.db.Raw("SELECT * FROM verification_codes WHERE email = ?", email).Scan(&prev).Error
	if prev.Email != "" && now-prev.SentAt < int64(resendWindow.Seconds()) {
		left := int(int64(resendWindow.Seconds()) - (now - prev.SentAt)) + 1
		return "", left, fmt.Errorf("发送太频繁，请 %d 秒后再试", left)
	}
	// 6 位数字验证码
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", 0, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	// 顺带清理过期验证码
	_ = v.db.Exec("DELETE FROM verification_codes WHERE expire_at < ?", now).Error
	if err := v.db.Exec(`
		INSERT INTO verification_codes (email, code, expire_at, sent_at, attempts)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(email) DO UPDATE SET code = excluded.code,
			expire_at = excluded.expire_at, sent_at = excluded.sent_at, attempts = 0`,
		email, code, now+int64(codeTTL.Seconds()), now).Error; err != nil {
		return "", 0, fmt.Errorf("验证码存储失败: %v", err)
	}

	if v.smtp == nil || v.smtp.Host == "" {
		// 开发模式：不发邮件，验证码进日志 + 返回给调用方
		slog.Warn("SMTP 未配置，验证码以开发模式返回", "email", email, "code", code)
		return code, 0, nil
	}
	if err := sendMail(v.smtp, email, "token 中转站注册验证码",
		fmt.Sprintf("你的注册验证码是：%s\n\n5 分钟内有效。若非本人操作请忽略本邮件。\n—— token 中转站", code)); err != nil {
		_ = v.db.Exec("DELETE FROM verification_codes WHERE email = ?", email).Error
		return "", 0, fmt.Errorf("邮件发送失败: %v", err)
	}
	return "", 0, nil
}

// Verify 校验并消费验证码（一次性）。尝试计数与消费均为原子条件写：
// 并发错码不丢计数（读-改-写会让 5 次上限形同虚设），并发对码只成功一次。
func (v *Verification) Verify(email, code string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	var row verifyRow
	if err := v.db.Raw("SELECT * FROM verification_codes WHERE email = ?", email).Scan(&row).Error; err != nil || row.Email == "" {
		return fmt.Errorf("请先获取验证码")
	}
	if time.Now().Unix() > row.ExpireAt {
		_ = v.db.Exec("DELETE FROM verification_codes WHERE email = ?", email).Error
		return fmt.Errorf("验证码已过期，请重新获取")
	}
	if row.Code != code {
		// 原子自增 + 上限封口：attempts 达 5 后不再增长，靠下面的复读作废
		res := v.db.Exec(`UPDATE verification_codes SET attempts = attempts + 1
			WHERE email = ? AND attempts < ?`, email, maxAttempts)
		if res.Error == nil && res.RowsAffected > 0 {
			var attempts int
			_ = v.db.Raw("SELECT attempts FROM verification_codes WHERE email = ?", email).Scan(&attempts).Error
			if attempts >= maxAttempts {
				_ = v.db.Exec("DELETE FROM verification_codes WHERE email = ?", email).Error
				return fmt.Errorf("错误次数过多，验证码已作废，请重新获取")
			}
		}
		return fmt.Errorf("验证码不正确")
	}
	// 原子消费：删得到行才算验证成功（并发同码请求只有一个能删到）
	res := v.db.Exec("DELETE FROM verification_codes WHERE email = ? AND code = ?", email, row.Code)
	if res.Error != nil || res.RowsAffected == 0 {
		return fmt.Errorf("验证码不正确")
	}
	return nil
}

// RegisterCompany 客户自助注册：验证码校验通过后创建客户（额度 0）+ 首任管理员
func (v *Verification) RegisterCompany(db *gorm.DB, orgName, email, code, username, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if orgName == "" || len(orgName) > 64 {
		return fmt.Errorf("请填写客户名（64 字以内）")
	}
	if len(username) < 3 {
		return fmt.Errorf("管理员账号至少 3 位")
	}
	if len(password) < 6 {
		return fmt.Errorf("密码至少 6 位")
	}
	if err := v.Verify(email, code); err != nil {
		return err
	}
	var cnt int64
	_ = db.Model(&model.Org{}).Where("name = ?", orgName).Count(&cnt).Error
	if cnt > 0 {
		return fmt.Errorf("客户名已被注册")
	}
	_ = db.Model(&model.User{}).Where("username = ? OR (email != '' AND email = ?)", username, email).Count(&cnt).Error
	if cnt > 0 {
		return fmt.Errorf("账号或邮箱已被使用")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		org := model.Org{Name: orgName, Remark: "自助注册 · 邮箱 " + email, QuotaLimit: 0, Status: 1}
		if err := tx.Create(&org).Error; err != nil {
			return err
		}
		return tx.Create(&model.User{
			OrgID: &org.ID, Username: username, PasswordHash: hash,
			DisplayName: orgName + " 管理员", Email: email,
			Role: model.RoleOrgAdmin, Status: 1,
		}).Error
	})
}

// sendMail 标准库 SMTP 发送；465 端口走 SSL，其余走 STARTTLS
func sendMail(cfg *config.Smtp, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	msg := []byte("To: " + to + "\r\n" +
		"From: " + cfg.From + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if cfg.Port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		defer c.Close()
		if err = c.Auth(auth); err != nil {
			return err
		}
		if err = c.Mail(cfg.From); err != nil {
			return err
		}
		if err = c.Rcpt(to); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err = w.Write(msg); err != nil {
			return err
		}
		if err = w.Close(); err != nil {
			return err
		}
		return c.Quit()
	}
	return smtp.SendMail(addr, auth, cfg.From, []string{to}, msg)
}
