package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	"token-gateway/internal/model"
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
	pg.PUT("/orgs/:id/quota", h.SetOrgQuota)
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

// PUT /orgs/:id/quota 设值调整：差值入流水、0 合法（零额度）、负值 400、客户不存在 404
func TestSetOrgQuotaHandler(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	now := time.Now().Unix()
	_ = db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (41, 'setqh', 1000000, 1, ?, ?)`, now, now).Error
	_ = db.Exec(`INSERT INTO quota_grants (subject_type, subject_id, amount, remark, created_at)
		VALUES ('org', 41, 1000000, '初始额度', ?)`, now).Error

	put := func(body string, orgID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/platform/orgs/"+orgID+"/quota", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	if w := put(`{"limit":2500000,"remark":"设高"}`, "41"); w.Code != http.StatusOK {
		t.Fatalf("设高应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var limit, sumGrants int64
	_ = db.Raw("SELECT quota_limit FROM orgs WHERE id = 41").Scan(&limit).Error
	_ = db.Raw(`SELECT COALESCE(SUM(amount), 0) FROM quota_grants WHERE subject_type='org' AND subject_id=41`).Scan(&sumGrants).Error
	if limit != 2_500_000 || sumGrants != 2_500_000 {
		t.Fatalf("设高后 limit=%d Σgrants=%d，应均为 2,500,000", limit, sumGrants)
	}

	// 0 合法：零额度是明确的业务语义，不得被 required/校验吞掉
	if w := put(`{"limit":0,"remark":"清零"}`, "41"); w.Code != http.StatusOK {
		t.Fatalf("设 0 应 200，得 %d: %s", w.Code, w.Body.String())
	}
	_ = db.Raw("SELECT quota_limit FROM orgs WHERE id = 41").Scan(&limit).Error
	if limit != 0 {
		t.Fatalf("设 0 后 limit 应 0，得 %d", limit)
	}

	if w := put(`{"limit":-1}`, "41"); w.Code != http.StatusBadRequest {
		t.Fatalf("负值应 400，得 %d: %s", w.Code, w.Body.String())
	}
	if w := put(`{"limit":100}`, "999"); w.Code != http.StatusNotFound {
		t.Fatalf("客户不存在应 404，得 %d: %s", w.Code, w.Body.String())
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

// 负初始额度必须 400 拒绝：额度是"预算上限"语义，负值 = 客户一出生即欠费态
// （quota_used=0 >= 负 limit），且会写入负数 grant 污染 Σgrants==quota_limit 流水口径
func TestCreateOrgRejectNegativeQuota(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/platform/orgs", strings.NewReader(
		`{"name":"neg","admin_username":"boss2","admin_password":"pass123","quota_amount":-5}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("负初始额度应 400，得 %d: %s", w.Code, w.Body.String())
	}
	var orgs, negGrants int64
	_ = db.Raw(`SELECT COUNT(*) FROM orgs WHERE name='neg'`).Scan(&orgs).Error
	_ = db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE amount < 0`).Scan(&negGrants).Error
	if orgs != 0 || negGrants != 0 {
		t.Fatalf("拒绝后不应落库：orgs=%d neg_grants=%d", orgs, negGrants)
	}
}

// 定价页可用渠道标注：channel_count 只计「启用且有可用密钥」的渠道
// （池内启用 Key 或 legacy 密文），无密钥 / 渠道停用 / 池 Key 全禁用均不计
func TestListModelsChannelCount(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	h := NewHandler(db, nil, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.GET("/models", h.ListModels)

	for _, name := range []string{"m-live", "m-nokey", "m-off", "m-none"} {
		if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
			VALUES (?, 1, 1, 1, 0, 0)`, name).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedCh := func(id int64, name, keyEnc string, status int64) {
		if err := db.Exec(`INSERT INTO channels (id, name, base_url, upstream_key_enc, status, created_at, updated_at)
			VALUES (?, ?, 'https://up.example', ?, ?, 0, 0)`, id, name, keyEnc, status).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedCh(1, "c-live", "enc-legacy", 1)  // 启用 + legacy 密文
	seedCh(2, "c-nokey", "", 1)           // 启用但无任何密钥
	seedCh(3, "c-off", "enc-legacy", 0)   // 有密钥但渠道停用
	seedCh(4, "c-pool", "", 1)            // 启用 + 池内启用 Key
	seedCh(5, "c-pooldis", "", 1)         // 启用但池 Key 全禁用
	ab := func(cid int64, m string) {
		if err := db.Exec(`INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, ?)`, cid, m).Error; err != nil {
			t.Fatal(err)
		}
	}
	ab(1, "m-live")
	ab(2, "m-nokey")
	ab(3, "m-off")
	ab(4, "m-live")
	ab(5, "m-none")
	if err := db.Exec(`INSERT INTO channel_keys (channel_id, key_enc, status, created_at, updated_at)
		VALUES (4, 'k1', 1, 0, 0), (5, 'k2', 0, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/platform/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var rows []struct {
		Name         string `json:"name"`
		ChannelCount int64  `json:"channel_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Name] = r.ChannelCount
	}
	want := map[string]int64{"m-live": 2, "m-nokey": 0, "m-off": 0, "m-none": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channel_count 口径不符：got %v want %v", got, want)
	}
}

// 表单版上游模型拉取（建渠道前）：按聊天路径推导 /models、透传密钥、排序去空；缺密钥 400
func TestUpstreamModelsByForm(t *testing.T) {
	var gotAuth, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"b-model"},{"id":"a-model"},{"id":""}]}`))
	}))
	defer up.Close()

	engine, db, token := newPlatformEnv(t)
	h := NewHandler(db, nil, up.Client(), nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.POST("/upstream-models", h.UpstreamModelsByForm)

	do := func(body string) (*httptest.ResponseRecorder, func()) {
		before := gotPath
		req := httptest.NewRequest(http.MethodPost, "/api/platform/upstream-models", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w, func() {
			if gotPath == before && w.Code == http.StatusOK {
				t.Fatalf("上游未收到请求：path=%q", gotPath)
			}
		}
	}

	w, check := do(`{"base_url":"` + up.URL + `","path":"/v1/chat/completions","upstream_key":"sk-form"}`)
	check()
	if w.Code != http.StatusOK {
		t.Fatalf("拉取应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
		Count  int      `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Models, []string{"a-model", "b-model"}) || out.Count != 2 {
		t.Fatalf("模型列表应排序去空，得 %v count=%d", out.Models, out.Count)
	}
	if gotAuth != "Bearer sk-form" || gotPath != "/v1/models" {
		t.Fatalf("应透传密钥并推导出 /v1/models，得 auth=%q path=%q", gotAuth, gotPath)
	}

	// 聊天路径为空同样回退 /v1/models
	gotPath = ""
	w, check = do(`{"base_url":"` + up.URL + `","upstream_key":"sk-form"}`)
	check()
	if w.Code != http.StatusOK || gotPath != "/v1/models" {
		t.Fatalf("空路径应回退 /v1/models，得 %d path=%q: %s", w.Code, gotPath, w.Body.String())
	}

	// 缺密钥直接 400，不打上游
	gotPath = ""
	w, _ = do(`{"base_url":"` + up.URL + `"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺密钥应 400，得 %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "" {
		t.Fatalf("缺密钥不应请求上游，得 path=%q", gotPath)
	}
}

// 渠道保存一站式定价：模型行带价则未登记的自动建 models 行（启用、带价），
// 已登记的只更新填了价的字段；负价拒绝且整单回滚（渠道也不落库）
func TestChannelSyncModelPrices(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.POST("/channels", h.CreateChannel)
	g.PUT("/channels/:id", h.UpdateChannel)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/platform/channels", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	// 存量模型：已有输入价，渠道侧只补输出价
	if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m-exist', 100, 0, 1, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}

	w := post(`{"name":"c1","base_url":"https://up.example","vendor":"volc","upstream_key":"sk-1","models":[
		{"model_name":"m-new","input_price":3000000,"output_price":9000000},
		{"model_name":"m-exist","output_price":2000000},
		{"model_name":"m-free"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("创建应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var mo model.Model
	if err := db.Where("name = ?", "m-new").First(&mo).Error; err != nil {
		t.Fatalf("m-new 应被自动登记: %v", err)
	}
	if mo.InputPrice != 3000000 || mo.OutputPrice != 9000000 || mo.Status != 1 || mo.Vendor != "volc" {
		t.Fatalf("m-new 定价不符: %+v", mo)
	}
	mo = model.Model{} // First 复用 struct 会把旧主键拼进条件，先清空
	if err := db.Where("name = ?", "m-exist").First(&mo).Error; err != nil {
		t.Fatal(err)
	}
	if mo.InputPrice != 100 || mo.OutputPrice != 2000000 {
		t.Fatalf("m-exist 应只更新输出价、保留原输入价: %+v", mo)
	}
	var cnt int64
	db.Model(&model.Model{}).Where("name = ?", "m-free").Count(&cnt)
	if cnt != 0 {
		t.Fatal("未填价的模型不应被登记")
	}

	// 负价：整个事务回滚，渠道与模型都不落库
	w = post(`{"name":"c2","base_url":"https://up.example","upstream_key":"sk-2","models":[
		{"model_name":"m-neg","input_price":-1}]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("负价应失败，得 %d: %s", w.Code, w.Body.String())
	}
	db.Model(&model.Channel{}).Where("name = ?", "c2").Count(&cnt)
	if cnt != 0 {
		t.Fatal("负价时渠道不应落库")
	}
	db.Model(&model.Model{}).Where("name = ?", "m-neg").Count(&cnt)
	if cnt != 0 {
		t.Fatal("负价时模型不应登记")
	}

	// 更新渠道同样生效：给已登记模型补输入价
	req := httptest.NewRequest(http.MethodPut, "/api/platform/channels/1",
		strings.NewReader(`{"name":"c1","base_url":"https://up.example","models":[
			{"model_name":"m-exist","input_price":8000000}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	wPut := httptest.NewRecorder()
	engine.ServeHTTP(wPut, req)
	if wPut.Code != http.StatusOK {
		t.Fatalf("更新应 200，得 %d: %s", wPut.Code, wPut.Body.String())
	}
	mo = model.Model{}
	if err := db.Where("name = ?", "m-exist").First(&mo).Error; err != nil {
		t.Fatal(err)
	}
	if mo.InputPrice != 8000000 || mo.OutputPrice != 2000000 {
		t.Fatalf("更新应改输入价并保留输出价: %+v", mo)
	}
}

// 「没配密钥就没有模型」：无密钥建渠道/改渠道带模型一律 400；删掉最后一把 Key
// 自动清空该渠道的模型能力（模型定价与历史账单不受影响）
func TestChannelRequiresKeyForModels(t *testing.T) {
	engine, db, token := newPlatformEnv(t)
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.POST("/channels", h.CreateChannel)
	g.PUT("/channels/:id", h.UpdateChannel)
	g.DELETE("/channels/:id/keys/:kid", h.DeleteChannelKey)

	do := func(method, url, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// 无密钥建渠道（带模型）→ 400
	w := do(http.MethodPost, "/api/platform/channels",
		`{"name":"c-nokey","base_url":"https://up.example","models":[{"model_name":"m1"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无密钥建渠道应 400，得 %d: %s", w.Code, w.Body.String())
	}

	// 正常建：带密钥 + 模型
	w = do(http.MethodPost, "/api/platform/channels",
		`{"name":"c-ok","base_url":"https://up.example","upstream_key":"sk-1","models":[{"model_name":"m1"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("带密钥建渠道应 200，得 %d: %s", w.Code, w.Body.String())
	}

	// 无密钥存量渠道（预置模板形态）配模型 → 400
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, status, created_at, updated_at)
		VALUES (99, 'c-preset', 'https://up.example', 0, 0, 0)`).Error; err != nil {
		t.Fatal(err)
	}
	w = do(http.MethodPut, "/api/platform/channels/99",
		`{"name":"c-preset","base_url":"https://up.example","models":[{"model_name":"m1"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无密钥渠道配模型应 400，得 %d: %s", w.Code, w.Body.String())
	}

	// 有密钥渠道更新模型 → 200；随后删掉最后一把 Key → 能力被清空
	var kid int64
	if err := db.Raw("SELECT id FROM channel_keys WHERE channel_id = 1").Scan(&kid).Error; err != nil || kid == 0 {
		t.Fatalf("应有池 Key: id=%d err=%v", kid, err)
	}
	var abCount int64
	db.Raw("SELECT COUNT(*) FROM channel_abilities WHERE channel_id = 1").Scan(&abCount)
	if abCount != 1 {
		t.Fatalf("删 Key 前应有 1 条能力，得 %d", abCount)
	}
	w = do(http.MethodDelete, fmt.Sprintf("/api/platform/channels/1/keys/%d", kid), "")
	if w.Code != http.StatusOK {
		t.Fatalf("删 Key 应 200，得 %d: %s", w.Code, w.Body.String())
	}
	db.Raw("SELECT COUNT(*) FROM channel_abilities WHERE channel_id = 1").Scan(&abCount)
	if abCount != 0 {
		t.Fatalf("删最后一把 Key 后能力应清空，得 %d", abCount)
	}
	// models 行保留（历史账单锚定模型名）
	var mCount int64
	db.Raw("SELECT COUNT(*) FROM models WHERE name = 'm1'").Scan(&mCount)
	if mCount != 0 {
		t.Fatal("渠道没建模型时不应有 models 行（本例未填价）")
	}
}
