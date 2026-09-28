package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"token-gateway/internal/service"
)

// 客户额度流水：分页 / org_id 过滤 / 操作人用户名回显（operator_id 空 → ""）
func TestListQuotaGrants(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	ins := func(orgID, amount int64, op any, remark string) {
		t.Helper()
		if err := db.Exec(`INSERT INTO quota_grants (subject_type, subject_id, amount, remark, operator_id, created_at)
			VALUES ('org', ?, ?, ?, ?, ?)`, orgID, amount, remark, op, now).Error; err != nil {
			t.Fatalf("造流水失败: %v", err)
		}
	}
	for _, o := range []struct {
		id   int64
		name string
	}{{1, "甲公司"}, {2, "乙公司"}} {
		if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
			VALUES (?, ?, 0, 1, ?, ?)`, o.id, o.name, now, now).Error; err != nil {
			t.Fatalf("造 org 失败: %v", err)
		}
	}
	ins(1, 500_000, int64(1), "充值")     // root 操作
	ins(1, -100_000, nil, "退款冲减")      // 无操作人（系统）
	ins(2, 200_000, int64(1), "初始额度") // 另一家

	get := func(path string) (map[string]any, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out, w
	}

	out, w := get("/api/platform/quota-grants")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if out["total"].(float64) != 3 {
		t.Fatalf("total 应 3，得 %v", out["total"])
	}
	rows := out["list"].([]any)
	r0 := rows[0].(map[string]any) // ORDER BY id DESC：最后插入的在前
	if r0["org_name"] != "乙公司" || r0["operator"] != "root" {
		t.Fatalf("org_name/operator 回显错: %v", r0)
	}
	if rows[1].(map[string]any)["operator"] != "" {
		t.Fatalf("无操作人应空串，得 %v", rows[1])
	}

	// org_id 过滤
	out, _ = get("/api/platform/quota-grants?org_id=1")
	if out["total"].(float64) != 2 {
		t.Fatalf("org1 过滤应 2 条，得 %v", out["total"])
	}
	for _, r := range out["list"].([]any) {
		if r.(map[string]any)["org_name"] != "甲公司" {
			t.Fatalf("过滤后不应出现别家: %v", r)
		}
	}

	// 分页
	out, _ = get("/api/platform/quota-grants?page=2&page_size=2")
	if out["total"].(float64) != 3 || len(out["list"].([]any)) != 1 {
		t.Fatalf("第二页应 1 条 / total 3，得 %v", out)
	}
}

// 账单查询总览：每客户一行 + 合计；非法月份 400
func TestBillingOverviewHandler(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
		VALUES (1, '甲公司', 1000000, 300000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
		VALUES (2, '乙公司', 0, 0, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	sh := service.BillingLocation("Asia/Shanghai")
	s, _, _ := service.PeriodBounds(sh, "2026-08")
	if err := db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name, status, cost,
		prompt_tokens, completion_tokens, created_at) VALUES (1, 1, 1, 'm', 200, 300000, 0, 0, ?)`, s+100).Error; err != nil {
		t.Fatalf("造日志失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/platform/billing/overview?month=2026-08", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Month            string `json:"month"`
		TotalConsumption int64  `json:"total_consumption"`
		Rows             []struct {
			OrgID       int64 `json:"org_id"`
			Consumption int64 `json:"consumption"`
		} `json:"rows"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Month != "2026-08" || out.TotalConsumption != 300_000 {
		t.Fatalf("月/合计错: %s %d", out.Month, out.TotalConsumption)
	}
	if len(out.Rows) != 2 || out.Rows[0].OrgID != 1 || out.Rows[0].Consumption != 300_000 {
		t.Fatalf("应 2 行且消耗降序，得 %+v", out.Rows)
	}

	// 非法月份
	req = httptest.NewRequest(http.MethodGet, "/api/platform/billing/overview?month=2026-13", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法月份应 400，得 %d", w.Code)
	}
}
