package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
)

// newSubAdminEnv 在 newPlatformEnv 基础上再造一个子管理员（id=2, is_super=0）
// 并返回其 token——超管专属端点的 403 对照组
func newSubAdminEnv(t *testing.T) (*gin.Engine, *gorm.DB, string, string) {
	t.Helper()
	engine, db, superTok := newPlatformEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, is_super, status, created_at, updated_at)
		VALUES (2, NULL, 'deputy', 'x', 'platform_admin', 0, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造子管理员失败: %v", err)
	}
	subTok, err := auth.GenerateToken("test-secret", time.Hour, 2, "platform_admin", nil, "")
	if err != nil {
		t.Fatalf("生成子管 token 失败: %v", err)
	}
	return engine, db, superTok, subTok
}

func doReq(engine *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	engine.ServeHTTP(w, req)
	return w
}

// 子管理员：业务端点照常，超管专属端点（账号管理/审计）全 403
func TestSubAdminForbiddenOnSuperEndpoints(t *testing.T) {
	engine, _, _, subTok := newSubAdminEnv(t)
	if w := doReq(engine, http.MethodGet, "/api/platform/admins", subTok, ""); w.Code != http.StatusForbidden {
		t.Fatalf("子管理员 GET /admins 应 403，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodPost, "/api/platform/admins", subTok,
		`{"username":"x2","password":"pass123"}`); w.Code != http.StatusForbidden {
		t.Fatalf("子管理员 POST /admins 应 403，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodGet, "/api/platform/audit", subTok, ""); w.Code != http.StatusForbidden {
		t.Fatalf("子管理员 GET /audit 应 403，got %d", w.Code)
	}
	// 业务端点不受影响
	if w := doReq(engine, http.MethodGet, "/api/platform/quota-grants", subTok, ""); w.Code != http.StatusOK {
		t.Fatalf("子管理员业务端点应照常访问，got %d", w.Code)
	}
}

// 建号：恒为子管理员（is_super=0、org NULL）；重名/短密码/邮箱占用等 400
func TestCreateAdmin(t *testing.T) {
	engine, _, superTok, _ := newSubAdminEnv(t)

	w := doReq(engine, http.MethodPost, "/api/platform/admins", superTok,
		`{"username":"ops1","password":"pass123","display_name":"运维一号","email":"OPS1@X.com"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建号应 200，got %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		IsSuper int    `json:"is_super"`
		OrgID   *int64 `json:"org_id"`
		Email   string `json:"email"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Role != "platform_admin" || created.IsSuper != 0 || created.OrgID != nil {
		t.Fatalf("建号应为子管理员（role/is_super=0/org NULL），got %+v", created)
	}
	if created.Email != "ops1@x.com" {
		t.Fatalf("邮箱应归一小写，got %q", created.Email)
	}

	for name, body := range map[string]string{
		"重名":       `{"username":"ops1","password":"pass123"}`,
		"用户名撞超管":  `{"username":"root","password":"pass123"}`,
		"短密码":      `{"username":"ops2","password":"123"}`,
		"短用户名":     `{"username":"ab","password":"pass123"}`,
		"邮箱占用":     `{"username":"ops3","password":"pass123","email":"OPS1@x.com"}`,
		"邮箱格式错":    `{"username":"ops4","password":"pass123","email":"not-a-mail"}`,
		"缺用户名":     `{"password":"pass123"}`,
	} {
		if w := doReq(engine, http.MethodPost, "/api/platform/admins", superTok, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，got %d: %s", name, w.Code, w.Body.String())
		}
	}
}

// 列表：超管在前、query 模糊过滤
func TestListAdmins(t *testing.T) {
	engine, _, superTok, _ := newSubAdminEnv(t)
	w := doReq(engine, http.MethodGet, "/api/platform/admins", superTok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var page struct {
		List []struct {
			ID      int64 `json:"id"`
			IsSuper int   `json:"is_super"`
		} `json:"list"`
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Total != 2 || len(page.List) != 2 {
		t.Fatalf("应两个管理员，got total=%d len=%d", page.Total, len(page.List))
	}
	if page.List[0].IsSuper != 1 || page.List[0].ID != 1 {
		t.Fatalf("超管应排最前，got %+v", page.List[0])
	}
	w = doReq(engine, http.MethodGet, "/api/platform/admins?query=deput", superTok, "")
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Total != 1 {
		t.Fatalf("query 过滤应只剩 1 个，got %d", page.Total)
	}
}

// 更新：防自锁（对自己 400）、停用他人/改名生效、status=2 400、404、
// 双启用超管时互降允许、唯一启用超管只剩一人时降级被守卫拒
func TestUpdateAdminRules(t *testing.T) {
	engine, db, superTok, _ := newSubAdminEnv(t)

	// 自锁：一切对 /admins/1（自己）的写操作 400
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/1", superTok, `{"status":0}`); w.Code != http.StatusBadRequest {
		t.Fatalf("停用自己应 400，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/1", superTok, `{"is_super":false}`); w.Code != http.StatusBadRequest {
		t.Fatalf("降自己超管位应 400，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/1", superTok, `{"display_name":"改名"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("改自己显示名应 400，got %d", w.Code)
	}
	// 参数与归属
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/2", superTok, `{"status":2}`); w.Code != http.StatusBadRequest {
		t.Fatalf("status=2 应 400，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/99", superTok, `{"status":0}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，got %d", w.Code)
	}
	// 停用他人 + 改名 → 200 且落库
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/2", superTok, `{"status":0,"display_name":"副手"}`); w.Code != http.StatusOK {
		t.Fatalf("停用他人应 200，got %d: %s", w.Code, w.Body.String())
	}
	var name string
	var status int
	_ = db.Raw("SELECT display_name, status FROM users WHERE id = 2").Row().Scan(&name, &status)
	if status != 0 || name != "副手" {
		t.Fatalf("停用+改名应落库，got status=%d name=%q", status, name)
	}
	// 提升→降级往返：双启用超管时降对方允许
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/2", superTok, `{"is_super":true,"status":1}`); w.Code != http.StatusOK {
		t.Fatalf("提升+启用他人应 200，got %d: %s", w.Code, w.Body.String())
	}
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/2", superTok, `{"is_super":false}`); w.Code != http.StatusOK {
		t.Fatalf("双启用超管时降对方应 200，got %d: %s", w.Code, w.Body.String())
	}
	// 守卫：目标是被停用的超管、启用超管只剩操作者一人 → 降级被拒
	// （串行可达路径；并发互降竞态同款守卫，见 admins.go 注释）
	_ = db.Exec("UPDATE users SET is_super = 1, status = 0 WHERE id = 2") // 停用态超管
	if w := doReq(engine, http.MethodPut, "/api/platform/admins/2", superTok, `{"is_super":false}`); w.Code != http.StatusBadRequest {
		t.Fatalf("唯一启用超管只剩操作者时降级应被守卫拒 400，got %d: %s", w.Code, w.Body.String())
	}
}

// 重置他人密码：hash 变化；重置自己 400；不存在 404
func TestResetAdminPassword(t *testing.T) {
	engine, db, superTok, _ := newSubAdminEnv(t)

	if w := doReq(engine, http.MethodPost, "/api/platform/admins/1/reset-password", superTok,
		`{"new_password":"newpass1"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("重置自己应 400，got %d", w.Code)
	}
	var oldHash string
	_ = db.Raw("SELECT password_hash FROM users WHERE id = 2").Row().Scan(&oldHash)
	if w := doReq(engine, http.MethodPost, "/api/platform/admins/2/reset-password", superTok,
		`{"new_password":"newpass1"}`); w.Code != http.StatusOK {
		t.Fatalf("重置他人应 200，got %d: %s", w.Code, w.Body.String())
	}
	var newHash string
	_ = db.Raw("SELECT password_hash FROM users WHERE id = 2").Row().Scan(&newHash)
	if newHash == oldHash || newHash == "" {
		t.Fatal("密码 hash 应已变化")
	}
	if w := doReq(engine, http.MethodPost, "/api/platform/admins/99/reset-password", superTok,
		`{"new_password":"newpass1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，got %d", w.Code)
	}
	if w := doReq(engine, http.MethodPost, "/api/platform/admins/2/reset-password", superTok,
		`{"new_password":"123"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("短密码应 400，got %d", w.Code)
	}
}

// 审计查看：JOIN 出用户名、无 actor 行回退、scope 默认平台层、actor/method/时间窗筛选
func TestListAuditFiltersAndJoin(t *testing.T) {
	engine, db, superTok, _ := newSubAdminEnv(t)
	now := time.Now().Unix()
	type seedRow struct {
		actor  *int64
		method string
		path   string
		at     int64
	}
	for i, r := range []seedRow{
		{int64Ptr(1), "POST", "/api/platform/orgs", now - 100},
		{int64Ptr(2), "PUT", "/api/org/members/9", now - 50},
		{nil, "DELETE", "/api/platform/orgs/5", now - 10},
	} {
		var actor any
		if r.actor != nil {
			actor = *r.actor
		}
		if err := db.Exec(`INSERT INTO audit_logs (actor_id, actor, method, path, status, detail, ip, created_at)
			VALUES (?, 'platform_admin', ?, ?, 200, '', '127.0.0.1', ?)`, actor, r.method, r.path, r.at).Error; err != nil {
			t.Fatalf("造审计行 %d 失败: %v", i, err)
		}
	}

	var page struct {
		List []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"list"`
		Total int64 `json:"total"`
	}
	unmarshal := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("audit 应 200，got %d: %s", w.Code, w.Body.String())
		}
		_ = json.Unmarshal(w.Body.Bytes(), &page)
	}

	// 默认 scope：只见平台层两行；JOIN 出 username；无 actor 行回退空
	unmarshal(doReq(engine, http.MethodGet, "/api/platform/audit", superTok, ""))
	if page.Total != 2 {
		t.Fatalf("默认 scope 应只见平台层 2 行，got %d", page.Total)
	}
	byID := map[int64]string{}
	for _, r := range page.List {
		byID[r.ID] = r.Username
	}
	if byID[1] != "root" {
		t.Fatalf("JOIN 应带出 root，got %q", byID[1])
	}
	if _, ok := byID[3]; !ok || byID[3] != "" {
		t.Fatalf("无 actor 行应 username 空串回退，got %+v", byID)
	}

	// scope=all：三行全见
	unmarshal(doReq(engine, http.MethodGet, "/api/platform/audit?scope=all", superTok, ""))
	if page.Total != 3 {
		t.Fatalf("scope=all 应 3 行，got %d", page.Total)
	}
	// actor 模糊：deputy 只命中 org 层那行（需 scope=all）
	unmarshal(doReq(engine, http.MethodGet, "/api/platform/audit?scope=all&actor=deput", superTok, ""))
	if page.Total != 1 || page.List[0].ID != 2 {
		t.Fatalf("actor=deput 应只命中 id=2，got total=%d", page.Total)
	}
	// method 筛选（平台层 DELETE 1 行）
	unmarshal(doReq(engine, http.MethodGet, "/api/platform/audit?method=delete", superTok, ""))
	if page.Total != 1 || page.List[0].ID != 3 {
		t.Fatalf("method=DELETE 应只命中 id=3，got total=%d", page.Total)
	}
	// start/end 时间窗
	unmarshal(doReq(engine, http.MethodGet,
		fmt.Sprintf("/api/platform/audit?start=%d&end=%d", now-60, now), superTok, ""))
	if page.Total != 1 || page.List[0].ID != 3 {
		t.Fatalf("时间窗应只含 id=3，got total=%d", page.Total)
	}
}

func int64Ptr(v int64) *int64 { return &v }
