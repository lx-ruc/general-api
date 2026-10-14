package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 邮箱绑定/换绑链路的数据库级测试：SMTP 未配置 → 开发模式，验证码直接返回可断言。
// 全局 mailerCfg 复用 reset_test.go 的 withResetGlobals 改-还模式。

// newBindDB 临时库 + 目标用户 u1（id=100，未绑邮箱）+ 占用者 u2（id=101，已绑 taken@x.com）
func newBindDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/bind.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	for _, u := range []struct {
		id    int64
		name  string
		email string
	}{{100, "binder", ""}, {101, "taker", "taken@x.com"}} {
		if err := gdb.Exec(`INSERT INTO users (id, username, password_hash, display_name, email, role, status, created_at, updated_at)
			VALUES (?, ?, 'x', ?, ?, 'org_admin', 1, ?, ?)`, u.id, u.name, u.name, u.email, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return gdb
}

// 发码：开发模式建行返回验证码；60s 窗口；格式拒绝；查重三态文案
func TestSendBindCode(t *testing.T) {
	db := newBindDB(t)
	withResetGlobals(t, "")

	code, retry, err := SendBindCode(db, 100, "New@X.com ")
	if err != nil || retry != 0 || len(code) != 6 {
		t.Fatalf("发码失败: err=%v retry=%d code=%q", err, retry, code)
	}
	// 归一化：大写 + 首尾空格应落库为小写
	var row string
	_ = db.Raw("SELECT email FROM email_bind_codes WHERE user_id = 100").Scan(&row).Error
	if row != "new@x.com" {
		t.Fatalf("邮箱应归一化为小写，得 %q", row)
	}

	// 60 秒窗口：拒绝并带剩余秒数
	_, retry, err = SendBindCode(db, 100, "new@x.com")
	if err == nil || retry <= 0 || retry > 61 {
		t.Fatalf("重发窗口应拒绝且带剩余秒数，得 err=%v retry=%d", err, retry)
	}

	// 被他人占用 / 已是本人 / 格式非法
	if _, _, err := SendBindCode(db, 100, "taken@x.com"); err == nil || err.Error() != "该邮箱已被其他账号绑定" {
		t.Fatalf("占用文案不符: %v", err)
	}
	_ = db.Exec("UPDATE users SET email = 'self@x.com' WHERE id = 100").Error
	if _, _, err := SendBindCode(db, 100, "self@x.com"); err == nil || err.Error() != "该邮箱已是当前账号的绑定邮箱" {
		t.Fatalf("本人文案不符: %v", err)
	}
	if _, _, err := SendBindCode(db, 100, "not-an-email"); err == nil {
		t.Fatal("非法邮箱应报错")
	}

	// 重新发码作废旧码（以最新为准）：绕过窗口回拨 sent_at
	_ = db.Exec("UPDATE email_bind_codes SET sent_at = ?", time.Now().Unix()-120).Error
	code2, _, _ := SendBindCode(db, 100, "self2@x.com")
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM email_bind_codes WHERE user_id = 100").Scan(&cnt).Error
	if cnt != 1 || code2 == code {
		t.Fatalf("重新发码应只剩 1 行且为新码：cnt=%d code2=%q", cnt, code2)
	}
}

// 全流程：发码 → 绑定落库 → 站内信 → 历史未用重置令牌作废
func TestBindEmailFlow(t *testing.T) {
	db := newBindDB(t)
	withResetGlobals(t, "")

	// 预置一条该账号未用的重置令牌（换绑后必须作废，防旧邮箱链接跨换绑存续）
	now := time.Now().Unix()
	_ = db.Exec(`INSERT INTO password_reset_tokens (user_id, email, token_hash, expire_at, used_at, created_at)
		VALUES (100, 'old@x.com', 'deadbeef', ?, 0, ?)`, now+600, now).Error

	code, _, err := SendBindCode(db, 100, "fresh@x.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := BindEmail(db, 100, "fresh@x.com", code); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	var email string
	_ = db.Raw("SELECT email FROM users WHERE id = 100").Scan(&email).Error
	if email != "fresh@x.com" {
		t.Fatalf("users.email 未落库: %q", email)
	}
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM email_bind_codes WHERE user_id = 100").Scan(&cnt).Error
	if cnt != 0 {
		t.Fatal("验证码消费后应删除")
	}
	_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 100 AND type = 'email_bind'").Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("应有 1 条绑定站内信，得 %d", cnt)
	}
	var used int64
	_ = db.Raw("SELECT used_at FROM password_reset_tokens WHERE token_hash = 'deadbeef'").Scan(&used).Error
	if used == 0 {
		t.Fatal("历史未用重置令牌应被作废")
	}

	// 未获取验证码直接提交
	if err := BindEmail(db, 101, "any@x.com", "123456"); err == nil {
		t.Fatal("无验证码行应拒绝")
	}
}

// 错码 5 次作废；邮箱不一致拒绝（拿 A 邮箱的码绑 B 邮箱）
func TestBindEmailWrongCodeAndMismatch(t *testing.T) {
	db := newBindDB(t)
	withResetGlobals(t, "")

	code, _, _ := SendBindCode(db, 100, "a@x.com")

	// 邮箱不一致：拒绝且不消费
	if err := BindEmail(db, 100, "b@x.com", code); err == nil {
		t.Fatal("邮箱与验证码不匹配应拒绝")
	}
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM email_bind_codes WHERE user_id = 100").Scan(&cnt).Error
	if cnt != 1 {
		t.Fatal("不一致拒绝不应消费验证码")
	}

	// 错码 5 次：作废
	for i := 0; i < 5; i++ {
		if err := BindEmail(db, 100, "a@x.com", "000000"); err == nil {
			t.Fatal("错码应报错")
		}
	}
	_ = db.Raw("SELECT COUNT(*) FROM email_bind_codes WHERE user_id = 100").Scan(&cnt).Error
	if cnt != 0 {
		t.Fatal("错码达上限后验证码行应被删除")
	}
	if err := BindEmail(db, 100, "a@x.com", code); err == nil {
		t.Fatal("作废后正码也应失败")
	}
}

// 发码与确认之间邮箱被他人抢占：拒绝且提示重新获取
func TestBindEmailPreempted(t *testing.T) {
	db := newBindDB(t)
	withResetGlobals(t, "")

	code, _, _ := SendBindCode(db, 100, "race@x.com")
	// 他人先行绑定同一邮箱
	now := time.Now().Unix()
	_ = db.Exec(`INSERT INTO users (id, username, password_hash, display_name, email, role, status, created_at, updated_at)
		VALUES (102, 'racer', 'x', 'racer', 'race@x.com', 'org_admin', 1, ?, ?)`, now, now).Error

	if err := BindEmail(db, 100, "race@x.com", code); err == nil {
		t.Fatal("被抢占后绑定应失败")
	}
	var email string
	_ = db.Raw("SELECT email FROM users WHERE id = 100").Scan(&email).Error
	if email != "" {
		t.Fatalf("抢占失败后不应写入邮箱，得 %q", email)
	}
}

// 并发提交同一验证码：原子消费，恰好一次成功
func TestBindEmailConcurrentSingleUse(t *testing.T) {
	db := newBindDB(t)
	withResetGlobals(t, "")

	code, _, _ := SendBindCode(db, 100, "cc@x.com")

	var wg sync.WaitGroup
	start := make(chan struct{})
	okCount := int32(0)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := BindEmail(db, 100, "cc@x.com", code); err == nil {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("并发绑定应恰好成功 1 次，得 %d", okCount)
	}
}
