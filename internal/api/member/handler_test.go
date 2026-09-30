package member

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
	"token-gateway/internal/service"
)

type memberEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

// newMemberEnv 临时库 + org/member 账号 + 挂 JWT 鉴权的 CreateKey 路由
func newMemberEnv(t *testing.T) *memberEnv {
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
	orgID := int64(1)
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 1000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'm1', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}

	const secret = "test-secret"
	token, err := auth.GenerateToken(secret, time.Hour, 1, "member", &orgID, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	api := engine.Group("/api/member", middleware.JWTAuth(secret, db))
	h := NewHandler(db)
	api.POST("/keys", h.CreateKey)
	api.GET("/stats/usage", h.UsageBreakdown)
	api.GET("/usage", h.ListUsage)
	api.GET("/models", h.ListModels)
	api.GET("/models/available", h.ListAvailableModels)
	api.GET("/requests", h.ListRequests)
	api.POST("/requests", h.CreateRequest)
	return &memberEnv{engine: engine, db: db, token: token}
}

func (e *memberEnv) postCreate(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/member/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func TestCreateKeyExpiresAt(t *testing.T) {
	e := newMemberEnv(t)

	// 过去时刻 → 400
	past := time.Now().Add(-time.Hour).Unix()
	if w := e.postCreate(`{"name":"a","expires_at":` + strconv.FormatInt(past, 10) + `}`); w.Code != http.StatusBadRequest {
		t.Fatalf("过去时刻应 400，得 %d: %s", w.Code, w.Body.String())
	}

	// 缺省 → 永久（expired_at NULL），响应回显 null
	w := e.postCreate(`{"name":"b"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("缺省应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ExpiresAt *int64 `json:"expires_at"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ExpiresAt != nil {
		t.Fatalf("缺省应永久，得 %v", *resp.ExpiresAt)
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='b' AND expired_at IS NULL`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("永久密钥应落库 expired_at NULL，cnt=%d", cnt)
	}

	// 合法未来时刻 → 落库且响应回显
	future := time.Now().Add(48 * time.Hour).Unix()
	w = e.postCreate(`{"name":"c","expires_at":` + strconv.FormatInt(future, 10) + `}`)
	if w.Code != http.StatusOK {
		t.Fatalf("未来时刻应 200，得 %d: %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ExpiresAt == nil || *resp.ExpiresAt != future {
		t.Fatalf("响应应回显 expires_at=%d，得 %v", future, resp.ExpiresAt)
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='c' AND expired_at = ?`, future).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("过期时刻应落库，cnt=%d", cnt)
	}
}

// 归集已从子账号面移除：建 key 不再有成本中心概念（挂靠由客户管理员在密钥一览操作）。
// 旧客户端多传的 cost_center_id 应被静默忽略，key 落库为未归集
func TestCreateKeyIgnoresCostCenter(t *testing.T) {
	e := newMemberEnv(t)
	if w := e.postCreate(`{"name":"legacy","cost_center_id":1}`); w.Code != http.StatusOK {
		t.Fatalf("携带旧字段应仍 200（忽略），得 %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='legacy' AND cost_center_id IS NULL`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("key 应为未归集，cnt=%d", cnt)
	}
}

// ---------------- 多维用量统计 ----------------

// getUsage GET /api/member/stats/usage（可带 query）
func (e *memberEnv) getUsage(query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/member/stats/usage"+query, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// insertULog 插一行 usage_logs（tokens = 入 + 出，cost 直接给定）
func (e *memberEnv) insertULog(t *testing.T, userID, keyID int64, modelName string, pt, ct, cost, createdAt, status int64) {
	t.Helper()
	if err := e.db.Exec(`INSERT INTO usage_logs
		(org_id, user_id, api_key_id, model_name, prompt_tokens, completion_tokens, cost, status, created_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, keyID, modelName, pt, ct, cost, status, createdAt).Error; err != nil {
		t.Fatalf("造 usage_log 失败: %v", err)
	}
}

type usageBreakdownResp struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Today struct {
		Requests int64 `json:"requests"`
		Tokens   int64 `json:"tokens"`
		Errors   int64 `json:"errors"`
	} `json:"today"`
	Month struct {
		Requests int64 `json:"requests"`
		Tokens   int64 `json:"tokens"`
	} `json:"month"`
	Range struct {
		Requests int64 `json:"requests"`
		Tokens   int64 `json:"tokens"`
		Cost     int64 `json:"cost"`
	} `json:"range"`
	ByKey []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Requests int64  `json:"requests"`
		Tokens   int64  `json:"tokens"`
	} `json:"by_key"`
	ByModel []struct {
		Name     string `json:"name"`
		Requests int64  `json:"requests"`
	} `json:"by_model"`
}

func decodeUsageBreakdown(t *testing.T, body []byte) usageBreakdownResp {
	t.Helper()
	var resp usageBreakdownResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v %s", err, body)
	}
	return resp
}

// 覆盖：today/month/range 三口径、by_key（含零用量 key、排除他人）、
// by_model、自定义时间区间
func TestUsageBreakdown(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now()
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'm2', 'x', 'member', 1, ?, ?)`, now.Unix(), now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	// 造 key：k1=主力、k2=零用量、k3=他人（不得出现在任何维度）
	for _, k := range []struct {
		id   int64
		uid  int64
		name string
	}{{11, 1, "k1"}, {12, 1, "k2"}, {13, 2, "k3"}} {
		if err := e.db.Exec(`INSERT INTO api_keys (id, org_id, user_id, name, key_prefix, key_hash, status, created_at)
			VALUES (?, 1, ?, ?, 'prefix', ?, 1, ?)`, k.id, k.uid, k.name, fmt.Sprintf("hash-%d", k.id), now.Unix()).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 数据：今日 2 行（glm 成功 / deepseek 失败）、本月早些 1 行（glm）、上月 1 行、他人 1 行。
	// 「本月早些」在当月 1 号当天不存在（本月零点已 ≥ 今日零点），期望值随之动态计算
	todayTS := now.Unix() - 60
	prevMonthTS := now.AddDate(0, -1, 0).Unix()
	e.insertULog(t, 1, 11, "glm-5.2", 100, 50, 10, todayTS, 200)
	e.insertULog(t, 1, 11, "deepseek-v4", 200, 100, 20, todayTS, 500)
	e.insertULog(t, 1, 11, "glm-5.2", 10000, 5000, 1000, prevMonthTS, 200)
	e.insertULog(t, 2, 13, "glm-5.2", 999, 999, 999, todayTS, 200) // 他人 → 任何维度不得计入

	hasMonthEarly := false
	if ms := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()); ms.Unix() < todayTS-3600 {
		hasMonthEarly = true
		e.insertULog(t, 1, 11, "glm-5.2", 1000, 500, 100, ms.Unix()+60, 200)
	}
	monthReq, monthTokens, monthCost := int64(2), int64(450), int64(30)
	if hasMonthEarly {
		monthReq, monthTokens, monthCost = 3, 1950, 130
	}
	glmReq := int64(1)
	if hasMonthEarly {
		glmReq = 2
	}

	// 默认区间 = 当月
	resp := decodeUsageBreakdown(t, e.getUsage("").Body.Bytes())
	if resp.Range.Requests != monthReq || resp.Range.Tokens != monthTokens || resp.Range.Cost != monthCost {
		t.Fatalf("range 应只含本月本人行，得 req=%d tokens=%d cost=%d", resp.Range.Requests, resp.Range.Tokens, resp.Range.Cost)
	}
	if resp.Today.Requests != 2 || resp.Today.Tokens != 450 || resp.Today.Errors != 1 {
		t.Fatalf("today 应 2 行 450 tokens 1 失败，得 req=%d tokens=%d errors=%d", resp.Today.Requests, resp.Today.Tokens, resp.Today.Errors)
	}
	if resp.Month.Requests != monthReq || resp.Month.Tokens != monthTokens {
		t.Fatalf("month 与默认 range 同口径，得 req=%d tokens=%d", resp.Month.Requests, resp.Month.Tokens)
	}
	// by_key：k1 有量、k2 零用量也须出现、k3（他人）不得出现
	if len(resp.ByKey) != 2 || resp.ByKey[0].ID != 11 || resp.ByKey[0].Tokens != monthTokens {
		t.Fatalf("by_key 应含本人 2 个 key（含零用量）且 k1 聚合正确，得 %+v", resp.ByKey)
	}
	for _, k := range resp.ByKey {
		if k.ID == 13 {
			t.Fatal("他人 key 不得出现在 by_key")
		}
	}
	// by_model：本月内 glm 与 deepseek，按 tokens 降序
	if len(resp.ByModel) != 2 || resp.ByModel[0].Name != "glm-5.2" || resp.ByModel[0].Requests != glmReq {
		t.Fatalf("by_model 错误: %+v", resp.ByModel)
	}

	// 自定义区间（上月那一刻起）→ 另含上月行
	q := fmt.Sprintf("?start=%d&end=%d", prevMonthTS-1, now.Unix()+1)
	resp2 := decodeUsageBreakdown(t, e.getUsage(q).Body.Bytes())
	if resp2.Range.Requests != monthReq+1 || resp2.Range.Tokens != monthTokens+15000 {
		t.Fatalf("自定义区间应另含上月行，得 req=%d tokens=%d", resp2.Range.Requests, resp2.Range.Tokens)
	}
	// start/end 回显
	if resp2.Start != prevMonthTS-1 || resp2.End != now.Unix()+1 {
		t.Fatalf("区间应回显，得 [%d, %d)", resp2.Start, resp2.End)
	}
}

// ListUsage 按 key 过滤
func TestListUsageKeyFilter(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO api_keys (id, org_id, user_id, name, key_prefix, key_hash, status, created_at)
		VALUES (11, 1, 1, 'k1', 'p', 'h1', 1, ?), (12, 1, 1, 'k2', 'p', 'h2', 1, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	e.insertULog(t, 1, 11, "glm-5.2", 1, 1, 1, now, 200)
	e.insertULog(t, 1, 12, "glm-5.2", 1, 1, 1, now, 200)
	e.insertULog(t, 1, 12, "glm-5.2", 1, 1, 1, now, 200)

	req := httptest.NewRequest(http.MethodGet, "/api/member/usage?key_id=12", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("key_id=12 应 2 行，得 %d", resp.Total)
	}
}

// 我的额度读数：月上限与当月累计都返回给前端（跨月惰性清零的读侧：非当前账期按 0）
func TestListModelsMonthlyQuota(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	period := service.PeriodOf(service.BillingLoc(), now)
	prev := service.PeriodOf(service.BillingLoc(), now-31*86400)
	_ = e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, monthly_quota, monthly_period, monthly_cost, created_at, updated_at)
		VALUES (2, 1, 'm2', 'x', 'member', 1, 2000000, ?, 1740040, ?, ?)`, period, now, now).Error
	_ = e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, monthly_quota, monthly_period, monthly_cost, created_at, updated_at)
		VALUES (3, 1, 'm3', 'x', 'member', 1, 2000000, ?, 999999, ?, ?)`, prev, now, now).Error

	get := func(userID int64) map[string]any {
		t.Helper()
		orgID := int64(1)
		token, err := auth.GenerateToken("test-secret", time.Hour, userID, "member", &orgID, "")
		if err != nil {
			t.Fatalf("生成 token 失败: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/member/models", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		e.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("user %d 应 200，得 %d: %s", userID, w.Code, w.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	m2 := get(2)
	if m2["monthly_quota"] != float64(2_000_000) || m2["monthly_used"] != float64(1_740_040) {
		t.Fatalf("当前账期应返回月限与累计: quota=%v used=%v", m2["monthly_quota"], m2["monthly_used"])
	}
	m3 := get(3)
	if m3["monthly_quota"] != float64(2_000_000) || m3["monthly_used"] != float64(0) {
		t.Fatalf("过期账期累计应按 0 读: quota=%v used=%v", m3["monthly_quota"], m3["monthly_used"])
	}
	if m1 := get(1); m1["monthly_quota"] != float64(0) {
		t.Fatalf("未设月限应为 0: %v", m1["monthly_quota"])
	}
}

// 模型明细同口径：被拒尝试（403 未授权/404 不存在的零成本行）不出现在 by_model；
// by_key 与各汇总仍计全量（用户能看到自己 key 的失败情况）
func TestUsageBreakdownByModelExcludesRejected(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	e.insertULog(t, 1, 1, "m1", 10, 5, 60, now, 200)
	e.insertULog(t, 1, 1, "glm-5.3", 0, 0, 0, now, 403)
	e.insertULog(t, 1, 1, "ghost", 0, 0, 0, now, 404)

	// 显式区间：默认区间 [月初, now) 的 end 不含当前秒，同一秒内造的数据会被排除
	w := e.getUsage(fmt.Sprintf("?start=1&end=%d", now+60))
	if w.Code != http.StatusOK {
		t.Fatalf("stats/usage 应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var rep struct {
		ByModel []struct {
			Name string `json:"name"`
		} `json:"by_model"`
		Range struct {
			Requests int64 `json:"requests"`
		} `json:"range"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(rep.ByModel) != 1 || rep.ByModel[0].Name != "m1" {
		t.Fatalf("by_model 应只含 m1: %+v", rep.ByModel)
	}
	if rep.Range.Requests != 3 {
		t.Fatalf("区间总请求数仍应计全量 3，得 %d", rep.Range.Requests)
	}
}

// ---------------- 额度/模型申请 ----------------

// postRequest POST /api/member/requests
func (e *memberEnv) postRequest(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/member/requests", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// 申请单泛化：模型申请 kind=model 落库（已授权的剔除）、非法/下架模型 400、
// 全部已授权 400、空申请 400；额度申请（存量形态）仍走 kind=quota
func TestCreateRequestModelKind(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	// m1 已授权 / m2 未授权 / m3 已下架
	_ = e.db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 1000000, 1000000, 1, ?, ?), ('m2', 1000000, 1000000, 1, ?, ?), ('m3', 1000000, 1000000, 0, ?, ?)`,
		now, now, now, now, now, now).Error
	_ = e.db.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'm1', ?)`, now).Error

	// 模型申请：含已授权的 m1 → 静默剔除后只落 m2
	w := e.postRequest(`{"models":["m1","m2"],"reason":"项目需要"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("模型申请应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var lastReq struct {
		Kind       string
		ModelNames string
	}
	_ = e.db.Raw(`SELECT kind, model_names FROM quota_requests ORDER BY id DESC LIMIT 1`).Scan(&lastReq).Error
	if lastReq.Kind != "model" || lastReq.ModelNames != "m2" {
		t.Fatalf("应落 kind=model 且剔除已授权，得 kind=%q names=%q", lastReq.Kind, lastReq.ModelNames)
	}

	// 非法模型名 → 400 且不落库
	if w := e.postRequest(`{"models":["ghost"],"reason":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("未知模型应 400，得 %d", w.Code)
	}
	// 下架模型 → 400
	if w := e.postRequest(`{"models":["m3"],"reason":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("下架模型应 400，得 %d", w.Code)
	}
	// 全部已授权 → 400
	if w := e.postRequest(`{"models":["m1"],"reason":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("全部已授权应 400，得 %d", w.Code)
	}
	// 空申请 → 400
	if w := e.postRequest(`{"reason":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空申请应 400，得 %d", w.Code)
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM quota_requests`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("仅第一笔合法申请应落库，cnt=%d", cnt)
	}

	// 额度申请（存量形态）仍走 quota 分支
	if w := e.postRequest(`{"amount":50000,"reason":"加量"}`); w.Code != http.StatusOK {
		t.Fatalf("额度申请应 200，得 %d: %s", w.Code, w.Body.String())
	}
	_ = e.db.Raw(`SELECT kind FROM quota_requests ORDER BY id DESC LIMIT 1`).Scan(&lastReq.Kind).Error
	if lastReq.Kind != "quota" {
		t.Fatalf("额度申请应 kind=quota，得 %q", lastReq.Kind)
	}
}

// available 列表：全部启用模型 + 本人 granted 标记（下架模型不出现）
func TestListAvailableModels(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	_ = e.db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 1000000, 1000000, 1, ?, ?), ('m2', 1000000, 1000000, 1, ?, ?), ('m3', 1000000, 1000000, 0, ?, ?)`,
		now, now, now, now, now, now).Error
	_ = e.db.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'm1', ?)`, now).Error

	req := httptest.NewRequest(http.MethodGet, "/api/member/models/available", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var resp []struct {
		Name    string `json:"name"`
		Granted bool   `json:"granted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 {
		t.Fatalf("应只含启用模型 m1/m2，得 %+v", resp)
	}
	granted := map[string]bool{}
	for _, m := range resp {
		granted[m.Name] = m.Granted
	}
	if !granted["m1"] || granted["m2"] {
		t.Fatalf("granted 标记错误: %+v", granted)
	}
}

// waitForNotification 轮询等异步站内通知落库（提交申请的通知在 go func 里写）
func waitForNotification(t *testing.T, db *gorm.DB, userID int64, typ string) bool {
	t.Helper()
	for i := 0; i < 40; i++ {
		var cnt int64
		_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = ?", userID, typ).Scan(&cnt).Error
		if cnt > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// 子账号提交申请 → 客户管理员收到 request_pending 站内通知（顶栏铃铛），
// 标题含申请人、正文含额度与事由；提交人自己不收到
func TestCreateRequestNotifiesOrgAdmin(t *testing.T) {
	e := newMemberEnv(t)
	// 管理员（id=2）：通知扇出的目标角色
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'admin', 'x', 'org_admin', 1, ?, ?)`, time.Now().Unix(), time.Now().Unix()).Error; err != nil {
		t.Fatal(err)
	}
	w := e.postRequest(`{"amount":3000,"reason":"项目攻坚"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("提交申请应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if !waitForNotification(t, e.db, 2, "request_pending") {
		t.Fatal("客户管理员应收到待审批站内通知")
	}
	var row struct {
		Title string
		Body  string
	}
	_ = e.db.Raw("SELECT title, body FROM notifications WHERE user_id = 2 AND type = 'request_pending'").Scan(&row).Error
	if !strings.Contains(row.Title, "待审批") || !strings.Contains(row.Body, "3000") || !strings.Contains(row.Body, "项目攻坚") {
		t.Fatalf("通知内容应含申请人与事由：title=%q body=%q", row.Title, row.Body)
	}
	var selfCnt int64
	_ = e.db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 1").Scan(&selfCnt).Error
	if selfCnt != 0 {
		t.Fatalf("提交人不应收到 request_pending，实际 %d 条", selfCnt)
	}
}
