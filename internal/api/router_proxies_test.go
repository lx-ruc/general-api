package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// ---------- 信任代理白名单：ClientIP 只对可信来源解析 XFF ----------

// clientIPOf 构造带指定 RemoteAddr / X-Forwarded-For 的请求，读路由内看到的 ClientIP
func clientIPOf(t *testing.T, proxies []string, remoteAddr, xff string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyTrustedProxies(r, proxies)
	var got string
	r.GET("/ip", func(c *gin.Context) { got = c.ClientIP() })
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	return got
}

// 默认（未配置）只信回环：外部直连伪造 XFF 无效，本机反代带入的 XFF 有效。
// 不加白名单时任一客户端都可伪造 XFF 换 IP，绕过登录/验证码的按 IP 限流。
func TestTrustedProxiesDefaultLoopback(t *testing.T) {
	// 外部地址直连 + 伪造 XFF → 必须取连接地址，XFF 被忽略
	if got := clientIPOf(t, nil, "203.0.113.9:1234", "1.2.3.4"); got != "203.0.113.9" {
		t.Fatalf("非可信来源的 XFF 应被忽略，ClientIP=%s", got)
	}
	// 本机反代 → 信任其转发的 XFF（真实部署形态：Caddy 同机）
	if got := clientIPOf(t, nil, "127.0.0.1:9", "1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("可信回环反代的 XFF 应生效，ClientIP=%s", got)
	}
}

// 显式配置网段：仅该网段反代被信任
func TestTrustedProxiesConfigured(t *testing.T) {
	if got := clientIPOf(t, []string{"10.0.0.0/24"}, "10.0.0.5:9", "1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("白名单内反代的 XFF 应生效，ClientIP=%s", got)
	}
	if got := clientIPOf(t, []string{"10.0.0.0/24"}, "203.0.113.9:9", "1.2.3.4"); got != "203.0.113.9" {
		t.Fatalf("白名单外来源的 XFF 应被忽略，ClientIP=%s", got)
	}
}

// 非法配置回退仅回环，不因配置错误而全信
func TestTrustedProxiesInvalidFallsBack(t *testing.T) {
	if got := clientIPOf(t, []string{"not-a-cidr"}, "203.0.113.9:9", "1.2.3.4"); got != "203.0.113.9" {
		t.Fatalf("非法配置应回退仅回环（XFF 忽略），ClientIP=%s", got)
	}
}
