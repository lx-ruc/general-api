package database

import (
	"testing"

	"token-gateway/internal/config"
)

// 折扣功能下线：存量库的 orgs.discount_permille 列在 Migrate 时被幂等删除；
// 新库（schema 不再建列）与已删过的库重复迁移均不报错
func TestMigrateDropsLegacyDiscountColumn(t *testing.T) {
	gdb, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/legacy.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	if err := Migrate(gdb); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 造出存量库形态：带折扣列且挂着非默认值
	if err := gdb.Exec(`ALTER TABLE orgs ADD COLUMN discount_permille INTEGER NOT NULL DEFAULT 600`).Error; err != nil {
		t.Fatalf("模拟存量折扣列失败: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO orgs (id, name, status, created_at, updated_at)
		VALUES (1, 'legacy-org', 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("存量库迁移失败: %v", err)
	}
	if columnExists(gdb, "orgs", "discount_permille") {
		t.Fatal("迁移后 orgs.discount_permille 应被删除")
	}
	// 客户行仍在且可正常读写（删列不得破坏存量数据）
	var name string
	if err := gdb.Raw("SELECT name FROM orgs WHERE id = 1").Scan(&name).Error; err != nil || name != "legacy-org" {
		t.Fatalf("存量客户行应完好（legacy-org），得 %q err=%v", name, err)
	}

	// 幂等：已删过的库再次迁移不报错；新库路径（无此列）同样安全
	if err := Migrate(gdb); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	gdb2, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/fresh.db"})
	if err != nil {
		t.Fatalf("打开新库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb2.DB(); _ = sqlDB.Close() })
	if err := Migrate(gdb2); err != nil {
		t.Fatalf("新库迁移失败: %v", err)
	}
	if columnExists(gdb2, "orgs", "discount_permille") {
		t.Fatal("新库不应存在 discount_permille 列")
	}
}
