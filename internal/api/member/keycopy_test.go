package member

import (
	"encoding/json"
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
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

// keyEnv 构造启用 AES 的 member 密钥环境（复制完整密钥依赖 key_enc 加密存档）
func newMemberKeyEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
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
	// 本人 + 他人（越权目标）
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'me', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'other', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造他人失败: %v", err)
	}
	token, err := auth.GenerateToken("test-secret", time.Hour, 1, "member", &orgID, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	cipher, err := crypto.NewCipher(crypto.RandomKeyB64())
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	engine := gin.New()
	api := engine.Group("/api/member", middleware.JWTAuth("test-secret", db))
	h := NewHandler(db, cipher)
	api.POST("/keys", h.CreateKey)
	api.GET("/keys", h.ListKeys)
	api.POST("/keys/:id/copy", h.CopyKey)
	return engine, db, token
}

func doKeyReq(engine *gin.Engine, token, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// 创建写 key_enc（AES 密文 ≠ 明文）；列表只回打码串绝不回明文/密文；
// 复制端点回完整明文与创建时一致；存量无存档 400；他人密钥 404
func TestKeyMaskAndCopy(t *testing.T) {
	engine, db, token := newMemberKeyEnv(t)

	// 建钥：创建响应回明文一次
	w := doKeyReq(engine, token, http.MethodPost, "/api/member/keys", `{"name":"k"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建钥应 200，得 %d: %s", w.Code, w.Body)
	}
	var created struct {
		Key string `json:"key"`
		ID  int64  `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Key, "sk-") || created.ID == 0 {
		t.Fatalf("创建响应应含完整密钥，got %s", w.Body)
	}

	// 落库的是密文（≠ 明文），哈希行仍在
	var enc, hash string
	_ = db.Raw("SELECT key_enc FROM api_keys WHERE id = ?", created.ID).Scan(&enc).Error
	_ = db.Raw("SELECT key_hash FROM api_keys WHERE id = ?", created.ID).Scan(&hash).Error
	if enc == "" || enc == created.Key {
		t.Fatalf("key_enc 应为 AES 密文（非空且 ≠ 明文），got %q", enc)
	}
	if hash == "" || hash == created.Key {
		t.Fatal("key_hash 应为 SHA-256，不得是明文")
	}

	// 列表：打码串 + 不泄漏明文/密文
	w = doKeyReq(engine, token, http.MethodGet, "/api/member/keys", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, created.Key) || strings.Contains(body, enc) {
		t.Fatalf("密钥列表不得泄漏明文/密文：%s", body)
	}
	var list []struct {
		KeyMasked string `json:"key_masked"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || !strings.Contains(list[0].KeyMasked, "…") {
		t.Fatalf("列表应含打码串（前6…后4），got %+v", list)
	}

	// 复制：回完整明文（与创建一致）
	w = doKeyReq(engine, token, http.MethodPost, "/api/member/keys/"+itoa(created.ID)+"/copy", "")
	if w.Code != http.StatusOK {
		t.Fatalf("复制应 200，得 %d: %s", w.Code, w.Body)
	}
	var copied struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &copied)
	if copied.Key != created.Key {
		t.Fatalf("复制应回完整明文，got %q want %q", copied.Key, created.Key)
	}

	// 存量无存档（enc=''）→ 400 引导重建
	if err := db.Exec("UPDATE api_keys SET key_enc = '' WHERE id = ?", created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w = doKeyReq(engine, token, http.MethodPost, "/api/member/keys/"+itoa(created.ID)+"/copy", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("存量密钥复制应 400，得 %d: %s", w.Code, w.Body)
	}

	// 他人密钥 → 404（不泄漏存在性）
	var otherKey int64
	if err := db.Exec(`INSERT INTO api_keys (org_id, user_id, name, key_prefix, key_hash, key_enc, status, created_at)
		VALUES (1, 2, 'other', 'sk-xxxx', 'h2', 'e2', 1, ?)`, time.Now().Unix()).Error; err != nil {
		t.Fatal(err)
	}
	_ = db.Raw("SELECT id FROM api_keys WHERE user_id = 2").Scan(&otherKey).Error
	if w = doKeyReq(engine, token, http.MethodPost, "/api/member/keys/"+itoa(otherKey)+"/copy", ""); w.Code != http.StatusNotFound {
		t.Fatalf("他人密钥复制应 404，得 %d", w.Code)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
