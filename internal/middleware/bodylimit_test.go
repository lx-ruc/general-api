package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newBodyLimitEngine(limit int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/echo", MaxBody(limit), func(c *gin.Context) {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"err": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

// 已知 ContentLength 超限：预检直接 413，handler 不执行
func TestMaxBodyRejectsOversize(t *testing.T) {
	r := newBodyLimitEngine(64)
	big := bytes.Repeat([]byte(`a`), 200)
	req := httptest.NewRequest(http.MethodPost, "/api/echo", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应回 413，got %d", w.Code)
	}
}

// 正常小请求不受影响；超小 limit=0 表示关闭
func TestMaxBodyAllowsNormal(t *testing.T) {
	r := newBodyLimitEngine(1 << 20)
	req := httptest.NewRequest(http.MethodPost, "/api/echo", bytes.NewReader([]byte(`{"x":1}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("正常 body 应回 200，got %d body=%s", w.Code, w.Body)
	}

	r2 := newBodyLimitEngine(0)
	big := bytes.Repeat([]byte(`a`), 4096)
	req2 := httptest.NewRequest(http.MethodPost, "/api/echo", bytes.NewReader(big))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, req2)
	if w2.Code == http.StatusRequestEntityTooLarge {
		t.Fatal("limit=0 应关闭限制")
	}
}

// 未知长度（ContentLength=-1）走 MaxBytesReader 兜底：读超限时 Bind 报错回 400
func TestMaxBodyChunkedFallback(t *testing.T) {
	r := newBodyLimitEngine(64)
	var buf bytes.Buffer
	buf.WriteString(`{"a":"`)
	for i := 0; i < 300; i++ {
		buf.WriteString("bbbb")
	}
	buf.WriteString(`"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/echo", bytes.NewReader(buf.Bytes()))
	req.ContentLength = -1 // 模拟 chunked
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知长度超限应经 Bind 报错回 400，got %d", w.Code)
	}
}
