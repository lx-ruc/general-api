package platform

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
	"token-gateway/internal/service"
)

// newPlatformEnv 临时库 + 系统管理员身份，挂 POST /api/platform/orgs
func newPlatformEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, NULL, 'root', 'x', 'platform_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造系统管理员失败: %v", err)
	}

	const secret = "test-secret"
	token, err := auth.GenerateToken(secret, time.Hour, 1, "platform_admin", nil)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	pg := engine.Group("/api/platform", middleware.JWTAuth(secret, db))
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{})
	pg.POST("/orgs", h.CreateOrg)
	return engine, db, token
}

// 建客户初始额度入流水：Σgrants(org) == quota_limit 从第一天起成立
func TestCreateOrgInitialGrant(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/orgs", strings.NewReader(
		`{"name":"acme","admin_username":"boss","admin_password":"pass123","quota_amount":5000000}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("建客户应 200，得 %d: %s", w.Code, w.Body.String())
	}

	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='org' AND subject_id=1
		AND amount=5000000 AND remark='创建客户初始额度'`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("初始额度应入流水，cnt=%d", cnt)
	}
	var limit int64
	_ = db.Raw(`SELECT quota_limit FROM orgs WHERE id=1`).Scan(&limit).Error
	if limit != 5_000_000 {
		t.Fatalf("org limit 应 5,000,000，得 %d", limit)
	}
}

// 厂商账单对账：录入 → 差异/偏差率/>2% 标红/no_usage 笔数
func TestVendorBillDiff(t *testing.T) {
	engine, db, token := newPlatformEnv(t)

	// 复用脚手架库：补挂对账路由
	pg := engine.Routes()
	_ = pg
	h := NewHandler(db, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.GET("/vendor-bills", h.ListVendorBills)
	g.PUT("/vendor-bills", h.UpsertVendorBill)
	g.DELETE("/vendor-bills/:id", h.DeleteVendorBill)
	g.POST("/billing/snapshots", h.RunSnapshot)

	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, status, created_at, updated_at)
		VALUES (1, 'deepseek', 'https://x', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, status, created_at, updated_at)
		VALUES (2, 'glm', 'https://y', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	do := func(method, url, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// 我方 8 月（账期时区）按渠道记录：ch1 vendor_cost 10000（含 2 笔 no_usage），ch2 8000
	aug := time.Now().In(service.BillingLoc()).Format("2006-01")
	s, e, _ := service.PeriodBounds(service.BillingLoc(), aug)
	for _, r := range []struct{ ch, vendor, noUsage int64 }{
		{1, 6000, 0}, {1, 4000, 1}, {1, 0, 1}, {2, 8000, 0},
	} {
		if err := db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, channel_id, model_name,
			prompt_tokens, completion_tokens, cost, vendor_cost, no_usage, status, created_at)
			VALUES (1, 1, 1, ?, 'm', 10, 10, 0, ?, ?, 200, ?)`, r.ch, r.vendor, r.noUsage, s+100).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = e

	// 未录入时：has_bill=false
	w := do(http.MethodGet, "/api/platform/vendor-bills?period="+aug, "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"has_bill":false`) {
		t.Fatal("未录入渠道应 has_bill=false")
	}

	// 录入：ch1=9000（我方 10000 → 差 +1000，11%>2% 标红）；ch2=8000（差 0，不标红）
	if w := do(http.MethodPut, "/api/platform/vendor-bills",
		`{"period":"`+aug+`","channel_id":1,"billed_points":9000,"note":"厂商账单"}`); w.Code != http.StatusOK {
		t.Fatalf("录入失败: %d %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodPut, "/api/platform/vendor-bills",
		`{"period":"`+aug+`","channel_id":2,"billed_points":8000}`); w.Code != http.StatusOK {
		t.Fatalf("录入失败: %d %s", w.Code, w.Body.String())
	}

	w = do(http.MethodGet, "/api/platform/vendor-bills?period="+aug, "")
	body := w.Body.String()
	if !strings.Contains(body, `"diff":1000`) || !strings.Contains(body, `"diff_pct":11`) {
		t.Fatalf("ch1 差异应 +1000 / 11%%: %s", body)
	}
	if !strings.Contains(body, `"over_pct":true`) {
		t.Fatal("偏差 >2% 应标红 over_pct=true")
	}
	if !strings.Contains(body, `"no_usage_count":2`) {
		t.Fatalf("ch1 no_usage 应 2 笔: %s", body)
	}
	// ch2 差 0 不标红
	if !strings.Contains(body, `"over_pct":false`) {
		t.Fatalf("差异在 2%% 内应 over_pct=false: %s", body)
	}

	// 补跑快照端点：写入后 period_balances 有行（快照对象是 org，先造一家）
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
		VALUES (1, 'acme', 1000000, 0, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	w = do(http.MethodPost, "/api/platform/billing/snapshots", `{"period":"`+aug+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("快照补跑失败: %d %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM period_balances").Scan(&cnt).Error
	if cnt == 0 {
		t.Fatal("补跑快照应写入 period_balances")
	}
}
