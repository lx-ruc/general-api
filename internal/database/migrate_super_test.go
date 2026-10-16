package database

import (
	"testing"

	"token-gateway/internal/config"
)

// 存量库首次引入 is_super：尚无超管时把最早的 platform_admin 提升为超管；
// 幂等（重复 Migrate 不变）；已有超管时不再提升第二个
func TestMigratePromotesEarliestPlatformAdmin(t *testing.T) {
	gdb, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	if err := Migrate(gdb); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 造两个 platform_admin（老库形态：is_super 落列默认 0）
	for _, id := range []int64{1, 2} {
		if err := gdb.Exec(`INSERT INTO users (id, username, password_hash, role, status, is_super, created_at, updated_at)
			VALUES (?, ?, 'x', 'platform_admin', 1, 0, 0, 0)`, id, string(rune('a'+id-1))).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	assertSuper := func(want1, want2 int64) {
		t.Helper()
		var s1, s2 int64
		_ = gdb.Raw("SELECT is_super FROM users WHERE id = 1").Scan(&s1).Error
		_ = gdb.Raw("SELECT is_super FROM users WHERE id = 2").Scan(&s2).Error
		if s1 != want1 || s2 != want2 {
			t.Fatalf("is_super 应为 %d/%d，得 %d/%d", want1, want2, s1, s2)
		}
	}
	assertSuper(1, 0) // 最早者提升

	if err := Migrate(gdb); err != nil {
		t.Fatalf("三次迁移失败: %v", err)
	}
	assertSuper(1, 0) // 幂等：已有超管不再提升第二个

	// 新库路径：schema.sql 建表自带 is_super 列（默认 0）
	gdb2, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/new.db"})
	if err != nil {
		t.Fatalf("打开新库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb2.DB(); _ = sqlDB.Close() })
	if err := Migrate(gdb2); err != nil {
		t.Fatalf("新库迁移失败: %v", err)
	}
	if err := gdb2.Exec(`INSERT INTO users (id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 'solo', 'x', 'platform_admin', 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(gdb2); err != nil {
		t.Fatalf("新库二次迁移失败: %v", err)
	}
	var s int64
	_ = gdb2.Raw("SELECT is_super FROM users WHERE id = 1").Scan(&s).Error
	if s != 1 {
		t.Fatalf("新库唯一管理员也应被提升为超管，got %d", s)
	}
}
