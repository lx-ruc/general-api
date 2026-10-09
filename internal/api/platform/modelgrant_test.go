package platform

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/crypto"
	"token-gateway/internal/middleware"
)

// 模型开通：申请审批（T5）、回收级联（T6）、定价面板行集（T7）

// newModelGrantEnv 复用 platform 测试环境，另挂模型开通相关路由
func newModelGrantEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	engine, db, token := newPlatformEnv(t)
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.GET("/model-requests", h.ListModelRequests)
	g.PUT("/model-requests/:id", h.HandleModelRequest)
	g.GET("/orgs/:id/models", h.GetOrgModels)
	g.PUT("/orgs/:id/models", h.SetOrgModels)
	g.GET("/model-prices/:name", h.ListOrgModelPrices)
	return engine, db, token
}

func putModelGrantReq(engine *gin.Engine, token, path, body string) int {
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w.Code
}

// T5 审批：批准写 org_model_grants（granted_by=审批人）、驳回不落、重复审批 404、
// 批准后站内通知客户管理员
func TestHandleModelRequest(t *testing.T) {
	engine, db, token := newModelGrantEnv(t)
	now := time.Now().Unix()
	seedOrgWithAdmin(t, db, 9, "race-org", now)
	if err := db.Exec(`INSERT INTO org_model_requests (id, org_id, model_names, reason, status, created_at)
		VALUES (55, 9, 'm1,m2', '项目需要', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 1000000, 1000000, 1, ?, ?), ('m2', 1000000, 1000000, 1, ?, ?)`, now, now, now, now).Error; err != nil {
		t.Fatal(err)
	}

	if code := putModelGrantReq(engine, token, "/api/platform/model-requests/55", `{"action":"approve","reply":"ok"}`); code != http.StatusOK {
		t.Fatalf("批准应 200，得 %d", code)
	}
	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM org_model_grants WHERE org_id = 9`).Scan(&cnt).Error
	if cnt != 2 {
		t.Fatalf("批准后应有 2 行开通记录，得 %d", cnt)
	}
	// 重复审批 → 404（pending 条件更新防双审批）
	if code := putModelGrantReq(engine, token, "/api/platform/model-requests/55", `{"action":"approve"}`); code != http.StatusNotFound {
		t.Fatalf("重复审批应 404，得 %d", code)
	}
	// 客户管理员收到站内通知（异步 goroutine，轮询等待落库）
	if !waitForModelGrantNotification(t, db, 91) {
		t.Fatal("org_admin 应收到开通通知")
	}
	var notified int64
	_ = db.Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = 91 AND type = 'model_request'`).Scan(&notified).Error
	if notified != 1 {
		t.Fatalf("org_admin 应收到 1 条开通通知，得 %d", notified)
	}

	// 驳回分支：不落开通记录
	if err := db.Exec(`INSERT INTO org_model_requests (id, org_id, model_names, status, created_at)
		VALUES (56, 9, 'm1', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	if code := putModelGrantReq(engine, token, "/api/platform/model-requests/56", `{"action":"reject","reply":"暂不开放"}`); code != http.StatusOK {
		t.Fatalf("驳回应 200，得 %d", code)
	}
	_ = db.Raw(`SELECT COUNT(*) FROM org_model_grants WHERE org_id = 9 AND model_name = 'm1'`).Scan(&cnt).Error
	// m1 在 55 号单已开通，驳回 56 不影响已有开通
	if cnt != 1 {
		t.Fatalf("驳回不应新增开通记录，m1 行数=%d", cnt)
	}
}

// T6 手动回收级联：移除开通时该客户全部子账号的相应授权同步删除，他客户不受影响
func TestSetOrgModelsRecycleCascade(t *testing.T) {
	engine, db, token := newModelGrantEnv(t)
	now := time.Now().Unix()
	seedOrgWithAdmin(t, db, 9, "org-a", now)
	seedOrgWithAdmin(t, db, 8, "org-b", now)
	if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 1000000, 1000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	// 两个客户都开通 m1，各有一个子账号授权 m1
	for _, oid := range []int64{9, 8} {
		if err := db.Exec(`INSERT INTO org_model_grants (org_id, model_name, created_at) VALUES (?, 'm1', ?)`, oid, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at) VALUES
		(92, 9, 'a-staff', 'x', 'member', 1, ?, ?), (82, 8, 'b-staff', 'x', 'member', 1, ?, ?)`, now, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at)
		VALUES (92, 'm1', ?), (82, 'm1', ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	// org-a 整体替换为空 = 回收 m1
	if code := putModelGrantReq(engine, token, "/api/platform/orgs/9/models", `{"models":[]}`); code != http.StatusOK {
		t.Fatalf("回收应 200，得 %d", code)
	}
	var aStaff, bStaff, aGrant int64
	_ = db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 92`).Scan(&aStaff).Error
	_ = db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 82`).Scan(&bStaff).Error
	_ = db.Raw(`SELECT COUNT(*) FROM org_model_grants WHERE org_id = 9`).Scan(&aGrant).Error
	if aStaff != 0 {
		t.Fatalf("回收后本客户子账号授权应清零，得 %d", aStaff)
	}
	if bStaff != 1 {
		t.Fatalf("他客户同模型授权不应受影响，得 %d", bStaff)
	}
	if aGrant != 0 {
		t.Fatalf("回收后开通行应删除，得 %d", aGrant)
	}
	// 回收通知落库（异步 goroutine，轮询等待）
	if !waitForModelGrantNotification(t, db, 91) {
		t.Fatal("org-a 管理员应收到回收通知")
	}
	var n int64
	_ = db.Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = 91 AND type = 'model_request'`).Scan(&n).Error
	if n != 1 {
		t.Fatalf("org-a 管理员应收到 1 条回收通知，得 %d", n)
	}
}

// waitForModelGrantNotification 轮询等待异步站内通知落库（最多 1s）
func waitForModelGrantNotification(t *testing.T, db *gorm.DB, userID int64) bool {
	t.Helper()
	for i := 0; i < 40; i++ {
		var cnt int64
		_ = db.Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = 'model_request'`, userID).Scan(&cnt).Error
		if cnt > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// T7 定价面板行集：已开通但还没有任何子账号授权的客户也应出现（否则平台看不到审批效果）
func TestListOrgModelPricesIncludesGranted(t *testing.T) {
	engine, db, token := newModelGrantEnv(t)
	now := time.Now().Unix()
	seedOrgWithAdmin(t, db, 9, "opened-only", now)
	if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 1000000, 1000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO org_model_grants (org_id, model_name, created_at) VALUES (9, 'm1', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/platform/model-prices/m1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "opened-only") {
		t.Fatalf("已开通未授权客户应出现在定价面板: %s", w.Body.String())
	}
}

func seedOrgWithAdmin(t *testing.T, db *gorm.DB, oid int64, name string, now int64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (?, ?, 100000, 1, ?, ?)`, oid, name, now, now).Error; err != nil {
		t.Fatal(err)
	}
	// 管理员 id 规则：oid*10+1（9→91，8→81）
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (?, ?, ?, 'x', 'org_admin', 1, ?, ?)`, oid*10+1, oid, name+"-boss", now, now).Error; err != nil {
		t.Fatal(err)
	}
}
