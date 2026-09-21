package middleware

import (
	"testing"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
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

// 存量补洗：旧版本落库的明文密码/Key 一次性脱敏；再跑一遍幂等（0 行变更），
// 无敏感字段的行原样不动
func TestScrubAuditHistory(t *testing.T) {
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	seed := []string{
		`{"name":"o","admin_password":"Secret123"}`,                        // 明文密码
		`{"keys":["sk-vendor-a","sk-vendor-b"],"weight":2}`,                // 明文 Key 数组
		`{"name":"c","upstream_key":"k1` + "\n" + `k2"}`,                   // 多行上游 Key
		`{"username":"u","password":"***"}`,                                // 已脱敏
		`{"name":"m1","input_price":0}`,                                    // 无敏感字段
		``,                                                                  // 空明细
	}
	for i, d := range seed {
		if err := db.Exec("INSERT INTO audit_logs (actor, method, path, status, detail, ip, created_at) VALUES (?, 'POST', '/x', 200, ?, '127.0.0.1', 0)", "role"+string(rune('a'+i)), d).Error; err != nil {
			t.Fatalf("造审计行失败: %v", err)
		}
	}

	if n := ScrubAuditHistory(db); n != 3 {
		t.Fatalf("应补洗 3 行，got %d", n)
	}
	want := []string{
		`{"name":"o","admin_password":"***"}`,
		`{"keys":"***","weight":2}`,
		`{"name":"c","upstream_key":"***"}`,
		`{"username":"u","password":"***"}`,
		`{"name":"m1","input_price":0}`,
		``,
	}
	for i, w := range want {
		var got string
		if err := db.Raw("SELECT detail FROM audit_logs WHERE actor = ?", "role"+string(rune('a'+i))).Scan(&got).Error; err != nil || got != w {
			t.Fatalf("行 %d 补洗不符：got %s want %s (err=%v)", i, got, w, err)
		}
	}
	// 幂等：第二轮无行变更
	if n := ScrubAuditHistory(db); n != 0 {
		t.Fatalf("第二轮应 0 行变更，got %d", n)
	}
}
