package service

import (
	"encoding/json"
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

// 空库 overview 的 JSON 契约：by_org/by_user/by_model 必须是数组（不得 null/缺键），
// series 恒 7 点。前端看板模板裸读 by_org.length——全新部署首访曾因此 TypeError 白屏
func TestStatsOverviewEmptyArraysContract(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	for _, sc := range []Scope{{}, {OrgID: ptrInt64(1)}, {UserID: ptrInt64(1)}} {
		ov, err := StatsOverview(gdb, sc)
		if err != nil {
			t.Fatalf("空库 overview 不应报错: %v", err)
		}
		raw, _ := json.Marshal(ov)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"by_org", "by_user", "by_model", "series"} {
			v, ok := m[key]
			if !ok || string(v) == "null" {
				t.Fatalf("scope%+v: %s 必须是数组而非缺键/null，got %q", sc, key, string(v))
			}
			var arr []json.RawMessage
			if err := json.Unmarshal(v, &arr); err != nil {
				t.Fatalf("scope%+v: %s 必须是数组: %v", sc, key, err)
			}
		}
		if len(ov.Series) != 7 {
			t.Fatalf("series 应恒为 7 点（含补零日），got %d", len(ov.Series))
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }

// 模型 Top 只统计真实消耗：被拒尝试（403 未授权/404 不存在/429 限流的零成本行）
// 不得以 0 token 行上榜；总请求数与错误数仍计全量（运营口径）
func TestStatsByModelExcludesRejectedAttempts(t *testing.T) {
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
	if err := gdb.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name, status, error, prompt_tokens, completion_tokens, cost, created_at)
		VALUES (1, 1, 1, 'm1', 200, '', 10, 5, 60, ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"glm-5.3", "ghost"} {
		if err := gdb.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name, status, error, prompt_tokens, completion_tokens, cost, created_at)
			VALUES (1, 1, 1, ?, 403, 'model_not_allowed', 0, 0, 0, ?)`, m, now).Error; err != nil {
			t.Fatal(err)
		}
	}

	ov, err := StatsOverview(gdb, Scope{})
	if err != nil {
		t.Fatalf("StatsOverview 失败: %v", err)
	}
	if len(ov.ByModel) != 1 || ov.ByModel[0].Name != "m1" {
		t.Fatalf("ByModel 应只含 m1（被拒尝试不上榜）: %+v", ov.ByModel)
	}
	// 运营口径不回归：总请求数与错误数仍统计全部 3 条
	if ov.Total.Requests != 3 || ov.Total.Errors != 2 {
		t.Fatalf("总请求/错误应 3/2，得 %d/%d", ov.Total.Requests, ov.Total.Errors)
	}
}
