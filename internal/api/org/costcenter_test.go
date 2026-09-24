package org

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 造一笔已结算消耗（快照口径：usage_logs.cost_center_id 即归集真相）
func (e *orgEnv) seedUsage(t *testing.T, centerID *int64, cost int64, at int64) {
	t.Helper()
	if err := e.db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, cost_center_id, model_name,
		status, cost, prompt_tokens, completion_tokens, created_at)
		VALUES (1, 2, 1, ?, 'm1', 200, ?, 100, 50, ?)`, centerID, cost, at).Error; err != nil {
		t.Fatalf("造消耗失败: %v", err)
	}
}

// 词表生命周期：建/重名/归档/改派隔离/报表未归集置底与占比/无毛利字段
func TestCostCenterLifecycle(t *testing.T) {
	e := newOrgEnv(t)

	// 创建两个中心
	if w := e.do(http.MethodPost, "/api/org/cost-centers", `{"name":"AI客服"}`); w.Code != http.StatusOK {
		t.Fatalf("创建应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if w := e.do(http.MethodPost, "/api/org/cost-centers", `{"name":"数据分析"}`); w.Code != http.StatusOK {
		t.Fatalf("创建应 200，得 %d", w.Code)
	}
	// org 内重名拒绝
	if w := e.do(http.MethodPost, "/api/org/cost-centers", `{"name":"AI客服"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("重名应 400，得 %d", w.Code)
	}

	// 消耗：中心1（600）、中心2（300）、未归集（100）
	now := time.Now().Unix()
	c1, c2 := int64(1), int64(2)
	e.seedUsage(t, &c1, 600, now)
	e.seedUsage(t, &c2, 300, now)
	e.seedUsage(t, nil, 100, now)

	// 归档中心2：报表仍显示其数据（center_status=0）
	if w := e.do(http.MethodPut, "/api/org/cost-centers/2", `{"status":0}`); w.Code != http.StatusOK {
		t.Fatalf("归档应 200，得 %d: %s", w.Code, w.Body.String())
	}

	// 报表：未归集恒置底 + 占比 10%
	w := e.do(http.MethodGet, "/api/org/reports/cost-centers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("报表应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var rep struct {
		List []struct {
			CostCenterID *int64 `json:"cost_center_id"`
			CenterName   string `json:"center_name"`
			CenterStatus int    `json:"center_status"`
			Cost         int64  `json:"cost"`
		} `json:"list"`
		TotalCost      int64   `json:"total_cost"`
		UnallocCost    int64   `json:"unallocated_cost"`
		UnallocPct     float64 `json:"unallocated_pct"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解析报表失败: %v", err)
	}
	if len(rep.List) != 3 {
		t.Fatalf("应有 3 行（两中心+未归集），得 %d", len(rep.List))
	}
	if last := rep.List[len(rep.List)-1]; last.CostCenterID != nil || last.CenterName != "" || last.Cost != 100 {
		t.Fatalf("未归集应置底最后一行，得 %+v", last)
	}
	if rep.TotalCost != 1000 || rep.UnallocCost != 100 {
		t.Fatalf("合计应 1000/未归集 100，得 %d/%d", rep.TotalCost, rep.UnallocCost)
	}
	if rep.UnallocPct < 9.9 || rep.UnallocPct > 10.1 {
		t.Fatalf("未归集占比应 10%%，得 %v", rep.UnallocPct)
	}
	// 归档中心2 在报表中带 status=0（前端标"已归档"）
	archived := false
	for _, r := range rep.List {
		if r.CostCenterID != nil && *r.CostCenterID == 2 {
			archived = r.CenterStatus == 0
		}
	}
	if !archived {
		t.Fatal("归档中心应仍在报表且 center_status=0")
	}

	// org 视角无毛利字段：响应不含 vendor_cost / margin
	body := w.Body.String()
	for _, banned := range []string{"vendor_cost", "margin"} {
		if strings.Contains(body, banned) {
			t.Fatalf("org 报表不得含 %s 字段", banned)
		}
	}

	// 改派：中心1 的 key 改到中心2 —— 被归档中心拒绝
	if w := e.do(http.MethodPut, "/api/org/keys/999/cost-center", `{"cost_center_id":2}`); w.Code != http.StatusBadRequest {
		t.Fatalf("改派到归档中心应 400，得 %d", w.Code)
	}
	// 越权：org2 的中心 id 不可用（本 org 查不到 → 400）
	if w := e.do(http.MethodPut, "/api/org/keys/999/cost-center", `{"cost_center_id":888}`); w.Code != http.StatusBadRequest {
		t.Fatalf("他 org 中心应 400，得 %d", w.Code)
	}
}

// 越权改名：org1 管理员以 org2 的中心 id 请求 → 未找到
func TestCostCenterOrgIsolation(t *testing.T) {
	e := newOrgEnv(t)
	// org2 的中心（不属于本 token 的 org）
	if err := e.db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (2, 'other', 100, 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO cost_centers (id, org_id, name, status, created_at, updated_at)
		VALUES (50, 2, '他家的', 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}
	w := e.do(http.MethodPut, "/api/org/cost-centers/50", `{"name":"抢注"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("越权改名应 404，得 %d: %s", w.Code, w.Body.String())
	}
	// 无副作用：他 org 中心名未被改
	var name string
	_ = e.db.Raw(`SELECT name FROM cost_centers WHERE id = 50`).Scan(&name).Error
	if name != "他家的" {
		t.Fatalf("他 org 中心不应被改动，得 %q", name)
	}
}

// 被拒尝试不进成本报表：403 未授权 / 404 模型不存在 / 429 限流等零成本行
// 不得以 0 成本模型行出现（否则看起来像"用了未授权的模型"）；已计费的非 200 行保留
func TestCostCenterReportExcludesRejectedAttempts(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	ins := func(model string, status, cost int64) {
		t.Helper()
		if err := e.db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name,
			status, cost, prompt_tokens, completion_tokens, created_at)
			VALUES (1, 2, 1, ?, ?, ?, 0, 0, ?)`, model, status, cost, now).Error; err != nil {
			t.Fatalf("造日志失败: %v", err)
		}
	}
	ins("deepseek-chat", 200, 100) // 真实消耗
	ins("glm-5.3", 403, 0)         // 未授权模型的被拒尝试
	ins("gpt-6-astra", 404, 0)     // 不存在的模型名
	ins("deepseek-chat", 429, 0)   // 授权模型上的限流被拒（噪音）
	ins("glm-5-2", 499, 50)        // 客户端中断但已部分计费 —— 必须保留

	w := e.do(http.MethodGet, "/api/org/reports/cost-centers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("报表应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var rep struct {
		List []struct {
			ModelName string `json:"model_name"`
			Requests  int64  `json:"requests"`
			Cost      int64  `json:"cost"`
		} `json:"list"`
		TotalCost int64 `json:"total_cost"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解析报表失败: %v", err)
	}
	if len(rep.List) != 2 {
		t.Fatalf("应只剩 2 个模型行（真实消耗 + 部分计费），得 %d: %+v", len(rep.List), rep.List)
	}
	for _, r := range rep.List {
		if r.ModelName == "glm-5.3" || r.ModelName == "gpt-6-astra" {
			t.Fatalf("被拒尝试的模型不应出现在报表: %+v", r)
		}
		if r.ModelName == "deepseek-chat" && r.Requests != 1 {
			t.Fatalf("限流被拒不应计入请求数，deepseek-chat 行 requests 应 1，得 %d", r.Requests)
		}
	}
	if rep.TotalCost != 150 {
		t.Fatalf("合计应 150，得 %d", rep.TotalCost)
	}
}

// 月度汇总（Billing）的按模型明细同口径：被拒尝试不出现在 by_model
func TestBillingByModelExcludesRejectedAttempts(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	ins := func(model string, status, cost int64) {
		t.Helper()
		if err := e.db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, model_name,
			status, cost, prompt_tokens, completion_tokens, created_at)
			VALUES (1, 2, 1, ?, ?, ?, 0, 0, ?)`, model, status, cost, now).Error; err != nil {
			t.Fatalf("造日志失败: %v", err)
		}
	}
	ins("m1", 200, 100)
	ins("glm-5.3", 403, 0)
	ins("gpt-6-astra", 404, 0)

	w := e.do(http.MethodGet, "/api/org/billing", "")
	if w.Code != http.StatusOK {
		t.Fatalf("月度汇总应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var rep struct {
		ByModel []struct {
			Name string `json:"name"`
		} `json:"by_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解析月度汇总失败: %v", err)
	}
	if len(rep.ByModel) != 1 || rep.ByModel[0].Name != "m1" {
		t.Fatalf("by_model 应只剩 m1，得 %+v", rep.ByModel)
	}
}
