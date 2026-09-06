package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
)

// 邮件通知：异步发送，失败只记日志不影响业务；SMTP 未配置时开发模式写日志

var mailerCfg *config.Smtp

// SetMailer 启动时注入 SMTP 配置（nil/空 host = 未配置）
func SetMailer(cfg *config.Smtp) { mailerCfg = cfg }

func MailerConfigured() bool { return mailerCfg != nil && mailerCfg.Host != "" }

// NotifyOrgAdmins 向公司全部有邮箱的管理员发送通知，返回已投递（异步）人数。
// SMTP 未配置时不发送，内容写入服务日志（开发模式可验证文案）。
func NotifyOrgAdmins(db *gorm.DB, orgID int64, subject, body string) int {
	var emails []string
	_ = db.Raw(`SELECT email FROM users WHERE org_id = ? AND role = 'org_admin' AND email != ''`,
		orgID).Scan(&emails).Error
	if len(emails) == 0 {
		slog.Info("邮件通知跳过：公司管理员未留邮箱", "org_id", orgID, "subject", subject)
		return 0
	}
	if !MailerConfigured() {
		slog.Warn("邮件通知（开发模式，SMTP 未配置）", "org_id", orgID,
			"subject", subject, "to", emails, "body", body)
		return 0
	}
	for _, to := range emails {
		go func(to string) {
			if err := sendMail(mailerCfg, to, subject, body); err != nil {
				slog.Warn("邮件通知发送失败", "to", to, "err", err)
			} else {
				slog.Info("邮件通知已发送", "to", to, "subject", subject)
			}
		}(to)
	}
	return len(emails)
}

// QuotaGrantEmailBody 生成额度授权通知邮件正文
func QuotaGrantEmailBody(orgName, adminName string, amount, newLimit, used int64, remark string) string {
	sign := "+"
	if amount < 0 {
		sign = ""
	}
	yuan := fmt.Sprintf("%.4f", float64(amount)/1e6)
	limitStr := formatToken(newLimit) + " token"
	if newLimit <= 0 {
		limitStr = "0（不限额关闭）"
	}
	remarkLine := ""
	if remark != "" {
		remarkLine = fmt.Sprintf("备注：%s\n", remark)
	}
	return fmt.Sprintf(`%s，你好：

你的公司「%s」额度已由平台管理员更新。

%s%s token（折合 ¥%s）
%s当前额度上限：%s
已消耗：%s token
操作时间：%s

请登录管理台查看详情。如非预期，请尽快联系平台管理员。
—— token 中转站`,
		adminName, orgName, sign, formatToken(amount), yuan, remarkLine, limitStr, formatToken(used),
		time.Now().Format("2006-01-02 15:04:05"))
}

func formatToken(n int64) string {
	if n < 0 {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	// 千分位
	if len(s) > 3 {
		var out []byte
		for i, c := range []byte(s) {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ',')
			}
			out = append(out, c)
		}
		return string(out)
	}
	return s
}
