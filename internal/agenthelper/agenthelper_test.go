package agenthelper

// /agent-helper 托管测试：占位符注入、反代头还原、脚本内容完整性。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r)
	return r
}

func TestServesBootstrapWithBase(t *testing.T) {
	r := newEngine()
	req := httptest.NewRequest(http.MethodGet, "/agent-helper", nil)
	req.Host = "gw.example.com:8080"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "shellscript") {
		t.Fatalf("Content-Type 应为 shellscript: %s", ct)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("应禁用缓存（base 地址随请求变化）")
	}
	body := w.Body.String()
	if !strings.Contains(body, "BASE='http://gw.example.com:8080'") {
		t.Fatalf("引导器应注入请求地址:\n%s", body)
	}
	if strings.Contains(body, basePlaceholder) {
		t.Fatal("不应残留占位符")
	}
}

func TestProxyHeadersRespected(t *testing.T) {
	r := newEngine()
	req := httptest.NewRequest(http.MethodGet, "/agent-helper.mjs", nil)
	req.Host = "internal:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "api.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "https://api.example.com") {
		t.Fatal("应按反代头还原 https 对外地址")
	}
	if !strings.Contains(w.Body.String(), "DEFAULT_BASE = 'https://api.example.com'") {
		t.Fatal("脚本默认 base 应被注入")
	}
}

func TestEmbeddedScriptsComplete(t *testing.T) {
	// 两份静态资源关键字自检（防止误提交空文件/半截文件）
	for _, kw := range []string{"claude-code", "codex", "opencode", "crush", "factory-droid", "selftest"} {
		if !strings.Contains(helperJS, kw) {
			t.Fatalf("helper.mjs 缺少关键内容：%s", kw)
		}
	}
	for _, kw := range []string{"node", "exec"} {
		if !strings.Contains(bootstrapSH, kw) {
			t.Fatalf("bootstrap.sh 缺少关键内容：%s", kw)
		}
	}
}
