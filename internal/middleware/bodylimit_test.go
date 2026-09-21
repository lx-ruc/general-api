package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// 模拟 "先写完 body 再读响应" 的客户端（Python urllib / curl 等非全双工实现）：
// 超限时服务端若不读 body 直接回 413 关连接，内核 RST 会让客户端写中途断开，
// 读不到错误响应（Go 自家 http.Client 因全双工读写在 handler 返回响应时不受影响，
// 掩盖不了该缺陷，故用裸 socket 刻意复现受害客户端的行为）。
func TestMaxBodyOversizeWriteThenReadClient(t *testing.T) {
	const limit, size = 1 << 20, 6 << 20 // 超限 5MB，落在排水封顶（limit+8MB）内
	srv := httptest.NewServer(newBodyLimitEngine(limit))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	hdr := "POST /api/echo HTTP/1.1\r\nHost: t\r\nContent-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", size)
	if _, err := conn.Write([]byte(hdr)); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte(`a`), 64<<10)
	for written := int64(0); written < size; {
		n, werr := conn.Write(chunk)
		written += int64(n)
		if werr != nil {
			t.Fatalf("写 body 中途被断开（客户端读不到 413）: written=%d/%d err=%v", written, size, werr)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("客户端应能读到 413 而非连接被重置: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应回 413，got %d", resp.StatusCode)
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
