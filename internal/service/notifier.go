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

// NotifyOrgAdmins 向客户全部有邮箱的管理员发送通知，返回已投递（异步）人数。
// SMTP 未配置时不发送，内容写入服务日志（开发模式可验证文案）。
func NotifyOrgAdmins(db *gorm.DB, orgID int64, subject, body string) int {
	var emails []string
	_ = db.Raw(`SELECT email FROM users WHERE org_id = ? AND role = 'org_admin' AND email != ''`,
		orgID).Scan(&emails).Error
	if len(emails) == 0 {
		slog.Info("邮件通知跳过：客户管理员未留邮箱", "org_id", orgID, "subject", subject)
		return 0
	}
	sendToAll(emails, subject, body)
	return len(emails)
}

// NotifyPlatformAdmins 通知全部留了邮箱的系统管理员（如客户额度耗尽的续费线索）。
func NotifyPlatformAdmins(db *gorm.DB, subject, body string) int {
	var emails []string
	_ = db.Raw(`SELECT email FROM users WHERE role = 'platform_admin' AND email != ''`).Scan(&emails).Error
	if len(emails) == 0 {
		slog.Info("邮件通知跳过：系统管理员未留邮箱", "subject", subject)
		return 0
	}
	sendToAll(emails, subject, body)
	return len(emails)
}

// NotifyUserAndAdmins 子账号级告警扇出：本人（有邮箱时）+ 其客户全部管理员。
func NotifyUserAndAdmins(db *gorm.DB, orgID int64, memberEmail, subject, body string) int {
	emails := make([]string, 0, 4)
	if memberEmail != "" {
		emails = append(emails, memberEmail)
	}
	var admins []string
	_ = db.Raw(`SELECT email FROM users WHERE org_id = ? AND role = 'org_admin' AND email != ''`,
		orgID).Scan(&admins).Error
	emails = append(emails, admins...)
	if len(emails) == 0 {
		slog.Info("邮件通知跳过：子账号与客户管理员均未留邮箱", "org_id", orgID, "subject", subject)
		return 0
	}
	sendToAll(emails, subject, body)
	return len(emails)
}

func sendToAll(emails []string, subject, body string) {
	if !MailerConfigured() {
		slog.Warn("邮件通知（开发模式，SMTP 未配置）", "subject", subject, "to", emails, "body", body)
		return
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
}

// QuotaGrantEmailBody 生成额度授权通知邮件正文（元口径；ppy 由调用方取库内 settings 传入，
// 不在函数内再查一次库——同一通知里 amount/limit/used 必须用同一汇率折算）
func QuotaGrantEmailBody(orgName, adminName string, amount, newLimit, used int64, remark string, ppy int64) string {
	sign := "+"
	if amount < 0 {
		sign = ""
	}
	limitStr := YuanString(newLimit, ppy)
	if newLimit <= 0 {
		limitStr = "¥0（不限额关闭）"
	}
	remarkLine := ""
	if remark != "" {
		remarkLine = fmt.Sprintf("备注：%s\n", remark)
	}
	return fmt.Sprintf(`%s，你好：

你的客户「%s」额度已由系统管理员更新。

%s%s
%s当前额度上限：%s
已消耗：%s
操作时间：%s

请登录管理台查看详情。如非预期，请尽快联系系统管理员。
—— %s`,
		adminName, orgName, sign, YuanString(amount, ppy), remarkLine, limitStr, YuanString(used, ppy),
		time.Now().Format("2006-01-02 15:04:05"), BrandName)
}
