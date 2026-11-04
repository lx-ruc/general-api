package database

import (
	"testing"

	"token-gateway/internal/config"
)

// 视频中转引入的库变更：video_tasks 新表 + models.type 列 + 存量 embedding 名字回填。
// 覆盖新库（schema.sql 直接建）与二次 Migrate 幂等
func TestMigrateVideoTables(t *testing.T) {
	gdb, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	if err := Migrate(gdb); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 造一个名字含 embedding 的模型（回填目标）与两个普通模型
	for _, name := range []string{"text-embedding-v4", "glm-5", "vid-x"} {
		if err := gdb.Exec(`INSERT INTO models (name, type, status, created_at, updated_at)
			VALUES (?, 'chat', 1, 0, 0)`, name).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	typ := func(name string) string {
		var s string
		if err := gdb.Raw("SELECT type FROM models WHERE name = ?", name).Scan(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := typ("text-embedding-v4"); got != "embedding" {
		t.Fatalf("名字含 embedding 的模型应回填为 embedding，got %q", got)
	}
	if got := typ("glm-5"); got != "chat" {
		t.Fatalf("普通模型应保持 chat，got %q", got)
	}

	// video_tasks 表存在且可写（含 task_id 唯一约束）
	if err := gdb.Exec(`INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id,
		model_name, output_price, created_at, updated_at) VALUES ('cgt-1', 1, 1, 1, 1, 'vid-x', 0, 0, 0)`).Error; err != nil {
		t.Fatalf("video_tasks 应可写入: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id,
		model_name, output_price, created_at, updated_at) VALUES ('cgt-1', 1, 1, 1, 1, 'vid-x', 0, 0, 0)`).Error; err == nil {
		t.Fatal("task_id 唯一约束应拦截重复登记")
	}

	// 三次迁移幂等
	if err := Migrate(gdb); err != nil {
		t.Fatalf("三次迁移失败: %v", err)
	}
}

// 存量库路径：老库无 type 列（模拟列缺失由 alters 补上并回填）
func TestMigrateVideoTypeBackfillOnLegacyDB(t *testing.T) {
	gdb, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/legacy.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	if err := Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	// 造存量形态：删列模拟老库（SQLite 支持 DROP COLUMN）
	if err := gdb.Exec(`ALTER TABLE models DROP COLUMN type`).Error; err != nil {
		t.Fatalf("模拟老库删列失败: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO models (name, status, created_at, updated_at)
		VALUES ('bge-embedding-x', 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("存量库迁移失败: %v", err)
	}
	var s string
	_ = gdb.Raw("SELECT type FROM models WHERE name = 'bge-embedding-x'").Scan(&s).Error
	if s != "embedding" {
		t.Fatalf("存量库加列后名字回填应生效，got %q", s)
	}
}
