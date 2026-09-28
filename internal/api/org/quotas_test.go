package org

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// 配额下发流水（org 隔离）：只看本客户子账号的流水——别家客户的、以及
// subject_type='org' 的客户级流水都不可见；user_id 过滤 + 操作人回显
func TestOrgQuotaGrantsScoped(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, display_name, password_hash, role, status, created_at, updated_at)
		VALUES (3, 1, 'alice', '爱丽丝', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造子账号失败: %v", err)
	}
	// 别家客户与其子账号
	if err := e.db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (2, '别家', 0, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造别家 org 失败: %v", err)
	}
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (4, 2, 'mallory', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造别家子账号失败: %v", err)
	}
	ins := func(subjectType string, subjectID, amount int64, op any) {
		t.Helper()
		if err := e.db.Exec(`INSERT INTO quota_grants (subject_type, subject_id, amount, remark, operator_id, created_at)
			VALUES (?, ?, ?, 'r', ?, ?)`, subjectType, subjectID, amount, op, now).Error; err != nil {
			t.Fatalf("造流水失败: %v", err)
		}
	}
	ins("user", 3, 200_000, int64(2)) // 本客户：admin(2) 给 alice(3) 下发
	ins("user", 4, 999_999, int64(1)) // 别家子账号 —— 不可见
	ins("org", 1, 500_000, int64(1))  // 客户级流水（platform 视角）—— 不可见

	w := e.do(http.MethodGet, "/api/org/quota-grants", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Total float64 `json:"total"`
		List  []struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Operator    string `json:"operator"`
			Amount      int64  `json:"amount"`
		} `json:"list"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 1 || len(out.List) != 1 {
		t.Fatalf("应只见本客户 1 条，得 total=%v list=%v", out.Total, out.List)
	}
	r := out.List[0]
	if r.Username != "alice" || r.DisplayName != "爱丽丝" || r.Operator != "admin" || r.Amount != 200_000 {
		t.Fatalf("回显错: %+v", r)
	}

	// user_id 过滤：命中 / 未命中
	w = e.do(http.MethodGet, "/api/org/quota-grants?user_id=3", "")
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 1 {
		t.Fatalf("user_id=3 应 1 条，得 %v", out.Total)
	}
	w = e.do(http.MethodGet, "/api/org/quota-grants?user_id=4", "")
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 0 {
		t.Fatalf("别家 user_id 过滤应 0 条，得 %v", out.Total)
	}
}
