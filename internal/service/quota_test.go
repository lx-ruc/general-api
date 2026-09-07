package service

import (
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

func TestQuotaLedgerInvariant(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	oid := int64(1)
	uid := int64(1)
	if err := gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'u', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	// 断言器：Σgrants(user) == COALESCE(quota_limit, 0)
	sumGrants := func() int64 {
		var s int64
		_ = gdb.Raw(`SELECT COALESCE(SUM(amount), 0) FROM quota_grants WHERE subject_type='user' AND subject_id = ?`, uid).Scan(&s).Error
		return s
	}
	limitBase := func() int64 {
		var v int64
		_ = gdb.Raw(`SELECT COALESCE(quota_limit, 0) FROM users WHERE id = ?`, uid).Scan(&v).Error
		return v
	}
	assertInvariant := func(step string) {
		t.Helper()
		if g, l := sumGrants(), limitBase(); g != l {
			t.Fatalf("[%s] Σgrants=%d 但 COALESCE(limit,0)=%d，审计链断裂", step, g, l)
		}
	}

	// 不限 → 限额（消耗 0 起点）：差值 0，无流水但不变量成立
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("转限额失败: %v", err)
	}
	assertInvariant("初始转限额(0)")
	var cnt int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='user'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("差值 0 不应产生流水，得 %d 条", cnt)
	}

	// 追加 ×2
	for _, amt := range []int64{1_000_000, 500_000} {
		if err := AddUserQuota(gdb, oid, uid, amt, 9, "追加"); err != nil {
			t.Fatalf("追加失败: %v", err)
		}
		assertInvariant("追加")
	}
	if g, l := sumGrants(), limitBase(); g != 1_500_000 || l != 1_500_000 {
		t.Fatalf("追加后应 1,500,000，得 Σ=%d limit=%d", g, l)
	}

	// 同态重复调用（已限额再转限额）：无操作、无流水
	before := sumGrants()
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("同态调用失败: %v", err)
	}
	if after := sumGrants(); after != before {
		t.Fatalf("同态调用不应产生流水，%d → %d", before, after)
	}

	// 限额 → 不限：差值 −1,500,000
	if err := SetUserQuotaUnlimited(gdb, oid, uid, true, 9); err != nil {
		t.Fatalf("转不限失败: %v", err)
	}
	if g := sumGrants(); g != 0 {
		t.Fatalf("转不限后 Σgrants 应 0，得 %d", g)
	}
	var nullLimit *int64
	_ = gdb.Raw(`SELECT quota_limit FROM users WHERE id = ?`, uid).Scan(&nullLimit).Error
	if nullLimit != nil {
		t.Fatalf("转不限后 limit 应为 NULL，得 %v", *nullLimit)
	}

	// 制造消耗后 不限 → 限额：以当前消耗为起点
	if err := gdb.Exec(`UPDATE users SET quota_used = 800000 WHERE id = ?`, uid).Error; err != nil {
		t.Fatal(err)
	}
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("转限额失败: %v", err)
	}
	if g, l := sumGrants(), limitBase(); g != 800_000 || l != 800_000 {
		t.Fatalf("转限额应以消耗 800,000 为起点，得 Σ=%d limit=%d", g, l)
	}
	assertInvariant("消耗起点转限额")
}
