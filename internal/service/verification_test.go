package service

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// ValidEmail 是注册流程唯一的邮箱入口校验，直接拼进 SMTP 头（To:），
// 必须拒绝一切控制字符（邮件头注入）与空白变体。
func TestValidEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  bool
	}{
		{"正常邮箱", "user@example.com", true},
		{"带加号", "user+tag@example.co.uk", true},
		{"无点域名", "a@localhost", false},
		{"缺@", "userexample.com", false},
		{"缺点", "user@example", false},
		{"含空格", "a b@example.com", false},
		{"含制表符", "a\tb@example.com", false},
		// 邮件头注入：CRLF/CR/LF 可拆分/追加任意头（Bcc 等）
		{"CRLF注入", "a@b.com\r\nBcc:victim@x.com", false},
		{"CR注入", "a@b.com\rBcc:victim@x.com", false},
		{"LF注入", "a@b.com\nBcc:victim@x.com", false},
		{"头体分隔注入", "a@b.com\r\n\r\nbody", false},
		{"NUL", "a\x00b@example.com", false},
		{"其他控制符", "a\x01b@example.com", false},
		{"DEL", "a\x7fb@example.com", false},
		{"超长", string(make([]byte, 255)) + "@x.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidEmail(c.email); got != c.want {
				t.Errorf("ValidEmail(%q) = %v, want %v", c.email, got, c.want)
			}
		})
	}
}

// ---------- 验证码暴力尝试的原子计数 ----------

// newVerifyEnv 临时库 + 一条已知验证码
func newVerifyEnv(t *testing.T) (*Verification, *gorm.DB) {
	t.Helper()
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/verify.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO verification_codes (email, code, expire_at, sent_at, attempts)
		VALUES ('race@x.com', '123456', ?, ?, 0)`, now+300, now).Error; err != nil {
		t.Fatal(err)
	}
	return NewVerification(db, nil), db
}

// 并发错误尝试不得丢计数：8 路并发错码后，尝试数必须达上限并作废验证码
// （原缺陷：读-改-写非原子，并发下 attempts 少记，5 次上限形同虚设，可无限暴力猜码）
func TestVerifyConcurrentAttemptsAtomic(t *testing.T) {
	v, db := newVerifyEnv(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = v.Verify("race@x.com", "000000") // 错码
		}()
	}
	close(start)
	wg.Wait()

	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM verification_codes WHERE email = 'race@x.com'`).Scan(&cnt).Error
	if cnt != 0 {
		var attempts int
		_ = db.Raw(`SELECT attempts FROM verification_codes WHERE email = 'race@x.com'`).Scan(&attempts).Error
		t.Fatalf("并发 8 次错码后验证码应已作废（attempts 达上限），行仍在且 attempts=%d", attempts)
	}
}

// 并发正确验证必须只成功一次（一次性消费语义）
func TestVerifyConcurrentSingleConsumption(t *testing.T) {
	v, _ := newVerifyEnv(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	okCount := int32(0)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := v.Verify("race@x.com", "123456"); err == nil {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("并发正确验证应恰好成功 1 次，得 %d", okCount)
	}
}

// 并发同用户名注册：预检查（SELECT COUNT）拦不住竞态，败者撞 users.username 唯一索引，
// 必须拿到与预检查一致的友好文案——原缺陷：把驱动原始错误
// 「constraint failed: UNIQUE constraint failed: users.username (2067)」原样泄漏给注册方
func TestRegisterCompanyConcurrentDuplicateFriendly(t *testing.T) {
	v, db := newVerifyEnv(t)
	now := time.Now().Unix()
	emails := []string{"ca@x.com", "cb@x.com"}
	for _, e := range emails {
		if err := db.Exec(`INSERT INTO verification_codes (email, code, expire_at, sent_at, attempts)
			VALUES (?, '123456', ?, ?, 0)`, e, now+300, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, len(emails))
	for i, e := range emails {
		wg.Add(1)
		go func(i int, e string) {
			defer wg.Done()
			<-start
			errs[i] = v.RegisterCompany(db, fmt.Sprintf("并发客户%d", i), e, "123456", "sameuser", "Pass123456")
		}(i, e)
	}
	close(start)
	wg.Wait()

	var users int64
	_ = db.Raw(`SELECT COUNT(*) FROM users WHERE username='sameuser'`).Scan(&users).Error
	if users != 1 {
		t.Fatalf("并发同用户名应恰建 1 个账号，得 %d", users)
	}
	for _, err := range errs {
		if err == nil {
			continue // 胜者
		}
		msg := err.Error()
		if strings.Contains(msg, "constraint") || strings.Contains(msg, "SQLSTATE") {
			t.Fatalf("败者拿到驱动原始错误: %s", msg)
		}
		if msg != "账号或邮箱已被使用" {
			t.Fatalf("败者文案不符: %q", msg)
		}
	}
}
