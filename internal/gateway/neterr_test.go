package gateway

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// timeoutError 实现 net.Error.Timeout()=true 的最小错误类型
type timeoutError struct{}

func (timeoutError) Error() string   { return "net/http: timeout awaiting response headers" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }

// urlErr 模拟 http.Client.Do 的真实错误形态（*url.Error 包装内层）
func urlErr(err error) error {
	return &url.Error{Op: "Post", URL: "https://upstream.example/v1/chat/completions", Err: err}
}

func TestClassifyNetErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want netErrKind
		desc string // 中文描述关键词
	}{
		{"响应头超时(net.Error)", urlErr(timeoutError{}), netErrTimeout, "上游超时"},
		{"响应头超时(文本)", urlErr(errors.New("net/http: timeout awaiting response headers")), netErrTimeout, "上游超时"},
		{"拨号 i/o 超时(文本)", urlErr(errors.New("dial tcp 1.2.3.4:443: i/o timeout")), netErrTimeout, "上游超时"},
		{"上下文超时(文本)", urlErr(context.DeadlineExceeded), netErrTimeout, "上游超时"},
		{"连接被拒绝", urlErr(&net.OpError{Op: "dial", Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}), netErrRefused, "连接被拒绝"},
		{"DNS 解析失败", urlErr(errors.New(`dial tcp: lookup nosuch.invalid: no such host`)), netErrDNS, "域名解析失败"},
		{"TLS 证书错误", urlErr(errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")), netErrTLS, "TLS/证书错误"},
		{"其它网络错误", urlErr(errors.New("read: connection reset by peer")), netErrOther, "网络错误"},
	}
	for _, tc := range cases {
		kind, desc := classifyNetErr(tc.err)
		if kind != tc.want {
			t.Fatalf("%s: 类别应为 %v，got %v（err=%v）", tc.name, tc.want, kind, tc.err)
		}
		if !strings.Contains(desc, tc.desc) {
			t.Fatalf("%s: 描述应含 %q，got %q", tc.name, tc.desc, desc)
		}
	}
}

func TestDescribeNetErr(t *testing.T) {
	got := DescribeNetErr(urlErr(timeoutError{}))
	if !strings.Contains(got, "上游超时") || !strings.Contains(got, "原始错误") {
		t.Fatalf("探测文案应含中文分类与原始错误，got %q", got)
	}
	// 原始错误截断到 300：整体文案有界，管理台弹窗不被超长错误撑爆
	long := urlErr(errors.New(strings.Repeat("x", 2000)))
	if n := len(DescribeNetErr(long)); n > 600 {
		t.Fatalf("原始错误应截断，got len=%d", n)
	}
}
