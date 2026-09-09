package service

import (
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 模型用量 Top 不应包含 model_name 为空/NULL 的行（连模型都没解析出来的失败请求，不算任何模型的用量）
func TestStatsByModelExcludesEmptyModelName(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	if err := gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'u', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO api_keys (id, org_id, user_id, key_prefix, key_hash, status, created_at)
		VALUES (1, 1, 1, 'sk-t', 'h', 1, ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	// 正常调用 + 未解析出模型的失败请求（model_name 为空串；schema NOT NULL，不存在 NULL）
	seed := []string{"m1", ""}
	for i, m := range seed {
		if err := gdb.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name, status, prompt_tokens, completion_tokens, cost, created_at)
			VALUES (1, 1, 1, ?, 200, 10, 5, 60, ?)`, m, now-int64(i)).Error; err != nil {
			t.Fatal(err)
		}
	}

	ov, err := StatsOverview(gdb, Scope{})
	if err != nil {
		t.Fatalf("StatsOverview 失败: %v", err)
	}
	for _, g := range ov.ByModel {
		if g.Name == "" {
			t.Fatalf("ByModel 不应包含空模型名: %+v", ov.ByModel)
		}
	}
	if len(ov.ByModel) != 1 || ov.ByModel[0].Name != "m1" {
		t.Fatalf("ByModel 应只含 m1: %+v", ov.ByModel)
	}
	// 总请求数仍应统计全部 2 条（空模型行计入请求总数，只是不进模型维度）
	if ov.Total.Requests != 2 {
		t.Fatalf("总请求数应为 2，got %d", ov.Total.Requests)
	}
}
