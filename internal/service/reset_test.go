package service

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 密码找回链路的数据库级测试：SMTP 未配置 → 开发模式，重置链接直接返回可断言。
// 全局 siteBaseURL / mailerCfg 用改-还模式，不污染其他测试。

// newResetDB 临时库 + 一个绑定邮箱 bound@x.com 的账号（org_admin）
func newResetDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/reset.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	hash, err := auth.HashPassword("OldPass123")
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO users (org_id, username, password_hash, display_name, email, role, status, created_at, updated_at)
		VALUES (NULL, 'resetuser', ?, '重置测试员', 'bound@x.com', 'org_admin', 1, ?, ?)`,
		hash, now, now).Error; err != nil {
		t.Fatal(err)
	}
	return gdb
}

func withResetGlobals(t *testing.T, siteURL string) {
	t.Helper()
	oldURL, oldMailer := siteBaseURL, mailerCfg
	SetSiteURL(siteURL)
	SetMailer(nil) // 开发模式：不真的发信
	t.Cleanup(func() { siteBaseURL, mailerCfg = oldURL, oldMailer })
}

// tokenFromDevLink 从开发模式链接里抠出明文 token（…?token=xxx）
func tokenFromDevLink(t *testing.T, link string) string {
	t.Helper()
	i := strings.LastIndex(link, "token=")
	if i < 0 {
		t.Fatalf("dev_link 缺 token: %q", link)
	}
	return link[i+len("token="):]
}

// 绑定邮箱：建令牌行、链接形态正确；未绑定邮箱：直接报错且不建令牌（不消耗邮件服务）；
// 格式非法：直接报错；60 秒窗口：拒绝并带剩余秒数
func TestRequestPasswordReset(t *testing.T) {
	db := newResetDB(t)
	withResetGlobals(t, "https://gw.example.com")

	link, retry, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil || retry != 0 {
		t.Fatalf("绑定邮箱请求失败: err=%v retry=%d", err, retry)
	}
	if !strings.HasPrefix(link, "https://gw.example.com/#/reset-password?token=") {
		t.Fatalf("链接形态不符: %q", link)
	}
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM password_reset_tokens WHERE email = 'bound@x.com'").Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("应建 1 行令牌，得 %d", cnt)
	}

	// 60 秒窗口内再次请求：拒绝（剩余秒数 +1 上取整，与验证码同口径最大 61）
	_, retry, err = RequestPasswordReset(db, "bound@x.com")
	if err == nil || retry <= 0 || retry > 61 {
		t.Fatalf("重发窗口应拒绝且带剩余秒数，得 err=%v retry=%d", err, retry)
	}

	// 未绑定邮箱：直接报错、无令牌行
	_, retry, err = RequestPasswordReset(db, "nobody@x.com")
	if err == nil || retry != 0 {
		t.Fatalf("未绑定邮箱应直接报错: err=%v retry=%d", err, retry)
	}
	_ = db.Raw("SELECT COUNT(*) FROM password_reset_tokens WHERE email = 'nobody@x.com'").Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("未绑定邮箱不应产生令牌行，得 %d", cnt)
	}

	// 格式非法：如实报错
	if _, _, err := RequestPasswordReset(db, "not-an-email"); err == nil {
		t.Fatal("非法邮箱应报错")
	}
}

// 全流程：请求 → 链接 token 重置成功 → 新密码可校验；旧密码失效
func TestResetPasswordFlow(t *testing.T) {
	db := newResetDB(t)
	withResetGlobals(t, "")

	link, _, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromDevLink(t, link)

	if err := ResetPassword(db, token, "NewPass456"); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	var hash string
	_ = db.Raw("SELECT password_hash FROM users WHERE username = 'resetuser'").Scan(&hash).Error
	if auth.CheckPassword(hash, "NewPass456") != true {
		t.Fatal("重置后新密码应可校验")
	}
	if auth.CheckPassword(hash, "OldPass123") {
		t.Fatal("旧密码不应再有效")
	}

	// 令牌一次性：再次使用同 token 必须失败
	if err := ResetPassword(db, token, "Again789"); err == nil {
		t.Fatal("令牌复用应失败")
	}
	// 密码也不应被第二次改掉
	_ = db.Raw("SELECT password_hash FROM users WHERE username = 'resetuser'").Scan(&hash).Error
	if !auth.CheckPassword(hash, "NewPass456") {
		t.Fatal("复用失败后密码不应变化")
	}
}

// 过期令牌与短密码拒绝
func TestResetPasswordExpiredAndShort(t *testing.T) {
	db := newResetDB(t)
	withResetGlobals(t, "")

	link, _, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromDevLink(t, link)

	if err := ResetPassword(db, token, "12345"); err == nil {
		t.Fatal("短密码应拒绝")
	}
	// 令牌回拨到过期
	_ = db.Exec("UPDATE password_reset_tokens SET expire_at = ?", time.Now().Unix()-1).Error
	if err := ResetPassword(db, token, "NewPass456"); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
	// 未知令牌
	if err := ResetPassword(db, "no-such-token", "NewPass456"); err == nil {
		t.Fatal("未知令牌应拒绝")
	}
}

// 再次申请会把该账号历史未用令牌作废（以最新邮件为准）
func TestRequestInvalidatesOldTokens(t *testing.T) {
	db := newResetDB(t)
	withResetGlobals(t, "")

	link1, _, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil {
		t.Fatal(err)
	}
	token1 := tokenFromDevLink(t, link1)
	// 绕过 60 秒窗口：把最近一行的 created_at 回拨
	_ = db.Exec("UPDATE password_reset_tokens SET created_at = ?", time.Now().Unix()-120).Error
	link2, _, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil {
		t.Fatal(err)
	}
	token2 := tokenFromDevLink(t, link2)

	if err := ResetPassword(db, token1, "First123"); err == nil {
		t.Fatal("旧令牌应已被作废")
	}
	if err := ResetPassword(db, token2, "Second456"); err != nil {
		t.Fatalf("新令牌应可用: %v", err)
	}
}

// 并发用同一令牌重置：原子认领，恰好一次成功
func TestResetPasswordConcurrentSingleUse(t *testing.T) {
	db := newResetDB(t)
	withResetGlobals(t, "")

	link, _, err := RequestPasswordReset(db, "bound@x.com")
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromDevLink(t, link)

	var wg sync.WaitGroup
	start := make(chan struct{})
	okCount := int32(0)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := ResetPassword(db, token, "RacePass789"); err == nil {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("并发重置应恰好成功 1 次，得 %d", okCount)
	}
}
