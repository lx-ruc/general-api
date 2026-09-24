package scrub

import "testing"

// 客户侧错误文本消毒：地址类内容必须剥净，普通技术文本必须原样保留
func TestStr(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// 网络层错误（http.Client Do 返回的标准形态）——重点泄漏源
		{
			"net error with url and lookup",
			`Post "https://api.deepseek.com/v1/chat/completions": dial tcp: lookup api.deepseek.com: no such host`,
			`Post "[filtered]": dial tcp: lookup [filtered]: no such host`,
		},
		{
			"net error with ipv4",
			`Post "https://ark.cn-beijing.volces.com/api/v3/chat/completions": dial tcp 115.190.124.90:443: connect: connection refused`,
			`Post "[filtered]": dial tcp [filtered]: connect: connection refused`,
		},
		{
			"header timeout",
			`Get "http://10.0.0.8:9102/v1/models": net/http: timeout awaiting response headers`,
			`Get "[filtered]": net/http: timeout awaiting response headers`,
		},
		{
			"localhost",
			`dial tcp localhost:9102: connect: connection refused`,
			`dial tcp [filtered]: connect: connection refused`,
		},
		// 上游错误体里的文档链接
		{
			"doc url in upstream message",
			`Insufficient Balance. Please see https://platform.deepseek.com/api-docs to recharge`,
			`Insufficient Balance. Please see [filtered] to recharge`,
		},
		// 不该被误伤的常规文本
		{"model name dots", `model glm-4.5 not found, try qwen2.5-turbo`, `model glm-4.5 not found, try qwen2.5-turbo`},
		{"json parse", `invalid character 'x' looking for beginning of value`, `invalid character 'x' looking for beginning of value`},
		{"status text", `upstream e2e-ok returned 502 Bad Gateway`, `upstream e2e-ok returned 502 Bad Gateway`},
		{"quota msg", `employee quota exceeded, please contact your company admin`, `employee quota exceeded, please contact your company admin`},
		{"empty", ``, ``},
	}
	for _, tc := range cases {
		if got := Str(tc.in); got != tc.want {
			t.Errorf("%s:\n  in   = %s\n  got  = %s\n  want = %s", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestBytes(t *testing.T) {
	in := []byte(`{"error":{"message":"see https://open.bigmodel.cn/docs","type":"insufficient_quota"}}`)
	got := string(Bytes(in))
	want := `{"error":{"message":"see [filtered]","type":"insufficient_quota"}}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
