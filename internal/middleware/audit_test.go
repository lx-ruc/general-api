package middleware

import (
	"testing"
)

// 审计脱敏：一切以 password 结尾的字段（含 admin_password）、upstream_key、
// keys 数组（渠道 Key 池批量入参）的值都必须被 *** 替换，其余字段原样保留
func TestAuditMasking(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"登录密码", `{"username":"u","password":"secret1"}`, `{"username":"u","password":"***"}`},
		{"建客户管理员初始密码", `{"name":"o","admin_password":"Audit911","admin_username":"a"}`,
			`{"name":"o","admin_password":"***","admin_username":"a"}`},
		{"改密旧码", `{"old_password":"a1","new_password":"b2"}`, `{"old_password":"***","new_password":"***"}`},
		{"渠道上游 Key 多行", `{"name":"c","upstream_key":"k1:8\nk2"}`, `{"name":"c","upstream_key":"***"}`},
		{"Key 池批量数组", `{"keys":["sk-vendor-a","sk-vendor-b"],"weight":2}`,
			`{"keys":"***","weight":2}`},
		{"无关字段不动", `{"name":"m1","input_price":0,"status":1}`, `{"name":"m1","input_price":0,"status":1}`},
		{"带空格的 JSON", "{\"admin_password\": \" spaced \"}", "{\"admin_password\": \"***\"}"},
	}
	for _, c := range cases {
		if got := pwdRe.ReplaceAllString(c.in, `${1}"***"`); got != c.want {
			t.Errorf("[%s] 脱敏不符：\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}
