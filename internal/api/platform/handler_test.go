package platform

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	token, err := auth.GenerateToken(secret, time.Hour, 1, "platform_admin", nil, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	pg := engine.Group("/api/platform", middleware.JWTAuth(secret, db))
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil)
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
	h := NewHandler(db, nil, nil, nil)
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

// 并发审批同一份充值申请：条件 UPDATE 必须在事务内完成 pending 判定，
// 双击/双管理员的并发 approve 只允许到账一次（quota_limit 与 Σgrants 均不得双记）
func TestHandleRechargeConcurrentApproveOnce(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (9, 'race-org', 100000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO recharge_requests (id, org_id, amount, status, created_at)
		VALUES (55, 9, 777777, 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.PUT("/recharges/:id", h.HandleRecharge)

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPut, "/api/platform/recharges/55", strings.NewReader(`{"action":"approve"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	var limit int64
	if err := db.Raw("SELECT quota_limit FROM orgs WHERE id = 9").Scan(&limit).Error; err != nil {
		t.Fatal(err)
	}
	if limit != 100000+777777 {
		t.Errorf("并发审批后 quota_limit = %d, want 877777（到账只能一次）", limit)
	}
	var grants int64
	_ = db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='org' AND subject_id=9`).Scan(&grants).Error
	if grants != 1 {
		t.Errorf("流水条数 = %d, want 1", grants)
	}
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("成功响应数 = %d, want 1, codes=%v", ok, codes)
	}
}

// 删除客户必须连带清理其全部用户的管理面访问令牌（tgp_），否则令牌在账号删除后仍可调用管理 API
func TestDeleteOrgCleansAccessTokens(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (7, 'doomed-co', 1000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (70, 7, 'boss7', 'x', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO access_tokens (user_id, name, token_hash, prefix, status, created_at, updated_at)
		VALUES (70, 'ci', 'hash-70', 'tgp_y', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.DELETE("/orgs/:id", h.DeleteOrg)

	req := httptest.NewRequest(http.MethodDelete, "/api/platform/orgs/7", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM access_tokens WHERE user_id = 70`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("客户删除后其用户的访问令牌应一并清理，剩 %d", cnt)
	}
}

// 并发录入同一 (period, channel) 的厂商账单：原生 upsert 下不得撞 UNIQUE 约束回 500，
// 全部 200 且库里恰好一行（金额为最后落库者）
func TestUpsertVendorBillConcurrent(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, status, created_at, updated_at)
		VALUES (3, 'race-ch', 'https://z', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, nil, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.PUT("/vendor-bills", h.UpsertVendorBill)

	period := time.Now().In(service.BillingLoc()).Format("2006-01")
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"period":%q,"channel_id":3,"billed_points":%d}`, period, 1000+i)
			req := httptest.NewRequest(http.MethodPut, "/api/platform/vendor-bills", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("并发 upsert 应全部 200，第 %d 个得 %d", i, c)
		}
	}
	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM vendor_bills WHERE period = ? AND channel_id = 3`, period).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("并发 upsert 后应恰好一行，得 %d", cnt)
	}
	var pts int64
	_ = db.Raw(`SELECT billed_points FROM vendor_bills WHERE period = ? AND channel_id = 3`, period).Scan(&pts).Error
	if pts < 1000 || pts > 1007 {
		t.Fatalf("落库金额应为并发值之一，得 %d", pts)
	}
}

// 成本价（cost_input/output_price）与售卖价同样不允许为负
func TestModelCostPriceRejectsNegative(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	h := NewHandler(db, nil, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.POST("/models", h.CreateModel)
	g.PUT("/models/:id", h.UpdateModel)

	do := func(method, url, body string) int {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code
	}
	if c := do(http.MethodPost, "/api/platform/models",
		`{"name":"neg","input_price":1,"output_price":1,"cost_input_price":-5}`); c != http.StatusBadRequest {
		t.Fatalf("创建时负成本价应 400，got %d", c)
	}
	if err := db.Exec(`INSERT INTO models (id, name, input_price, output_price, status, created_at, updated_at)
		VALUES (1, 'm', 1, 1, 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}
	if c := do(http.MethodPut, "/api/platform/models/1",
		`{"input_price":1,"output_price":1,"cost_output_price":-9}`); c != http.StatusBadRequest {
		t.Fatalf("更新时负成本价应 400，got %d", c)
	}
}

// 天价充值审批：amount 使 quota_limit 回绕时审批整体拒绝（400），
// 申请保持 pending、额度与流水均不动，可改走驳回
func TestHandleRechargeOverflowRejected(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (11, 'ov-org', ?, 1, ?, ?)`, int64(1)<<62, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO recharge_requests (id, org_id, amount, status, created_at)
		VALUES (77, 11, ?, 'pending', ?)`, int64(1)<<62, now).Error; err != nil {
		t.Fatal(err)
	}
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.PUT("/recharges/:id", h.HandleRecharge)

	req := httptest.NewRequest(http.MethodPut, "/api/platform/recharges/77", strings.NewReader(`{"action":"approve"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("回绕审批应 400，got %d body=%s", w.Code, w.Body)
	}
	var status string
	_ = db.Raw("SELECT status FROM recharge_requests WHERE id = 77").Scan(&status).Error
	if status != "pending" {
		t.Fatalf("拒绝后申请应保持 pending，got %s", status)
	}
	var lim int64
	_ = db.Raw("SELECT quota_limit FROM orgs WHERE id = 11").Scan(&lim).Error
	if lim != int64(1)<<62 {
		t.Fatalf("quota_limit 不得变动，got %d", lim)
	}
	var grants int64
	_ = db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='org' AND subject_id=11`).Scan(&grants).Error
	if grants != 0 {
		t.Fatalf("不得留流水，got %d", grants)
	}
}
