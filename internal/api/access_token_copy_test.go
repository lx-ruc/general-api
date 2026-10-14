package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
)

// 访问令牌（tgp_）：创建写 token_enc；列表只回打码串；复制端点回完整明文；
// 存量无存档 400；非属主 404
func TestAccessTokenMaskAndCopy(t *testing.T) {
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
		VALUES (1, 1, 'admin', 'x', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}
	token, err := auth.GenerateToken("test-secret", time.Hour, 1, "org_admin", &orgID, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	cipher, err := crypto.NewCipher(crypto.RandomKeyB64())
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	engine := gin.New()
	tk := engine.Group("/api/me/tokens", middleware.JWTAuth("test-secret", db))
	h := &AccessTokenHandler{DB: db, Cipher: cipher}
	tk.GET("", h.List)
	tk.POST("", h.Create)
	tk.POST("/:id/copy", h.Copy)

	do := func(method, path, body string) *httptest.ResponseRecorder {
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

	// 建令牌：明文一次
	w := do(http.MethodPost, "/api/me/tokens", `{"name":"ci"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建令牌应 200，得 %d: %s", w.Code, w.Body)
	}
	var created struct {
		Token string `json:"token"`
		ID    int64  `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Token, "tgp_") || created.ID == 0 {
		t.Fatalf("创建响应应含完整令牌，got %s", w.Body)
	}

	// 列表：打码 + 无明文
	w = do(http.MethodGet, "/api/me/tokens", "")
	body := w.Body.String()
	if strings.Contains(body, created.Token) {
		t.Fatalf("令牌列表不得泄漏明文：%s", body)
	}
	var list []struct {
		TokenMasked string `json:"token_masked"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || !strings.Contains(list[0].TokenMasked, "…") {
		t.Fatalf("列表应含打码串，got %+v", list)
	}

	// 复制：回完整明文
	w = do(http.MethodPost, "/api/me/tokens/"+itoa64(created.ID)+"/copy", "")
	if w.Code != http.StatusOK {
		t.Fatalf("复制应 200，得 %d: %s", w.Code, w.Body)
	}
	var copied struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &copied)
	if copied.Key != created.Token {
		t.Fatalf("复制应回完整令牌，got %q want %q", copied.Key, created.Token)
	}

	// 存量无存档 → 400；非属主（他人令牌）→ 404
	if err := db.Exec("UPDATE access_tokens SET token_enc = '' WHERE id = ?", created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w = do(http.MethodPost, "/api/me/tokens/"+itoa64(created.ID)+"/copy", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("存量令牌复制应 400，得 %d", w.Code)
	}
	other := model.AccessToken{UserID: 999, Name: "x", TokenHash: "h9", Prefix: "tgp_zz", Status: 1}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if w = do(http.MethodPost, "/api/me/tokens/"+itoa64(other.ID)+"/copy", ""); w.Code != http.StatusNotFound {
		t.Fatalf("他人令牌复制应 404，得 %d", w.Code)
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
