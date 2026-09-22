package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

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

// 审计异步落库与 gin 对象池复用的竞争回归：请求一结束 gin 就把
// Context/ResponseWriter 放回 sync.Pool 供下一个连接 reset() 复用，审计
// goroutine 若在异步段再读 c.Writer.Status()/GetRole(c)/c.Request.* 就会与
// 复用写竞争（-race 必报），且审计行可能串台记成下一个请求的属性。
// 修复口径：全部字段在同步段取值，goroutine 只拿纯值。
// 竞争本身须以 `go test -race` 运行本测试才被检出；普通模式断言串台计数。
func TestAuditAsyncWriteOwnRequestAttributes(t *testing.T) {
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	// 每条路径绑定不同状态码：串台（把 B 请求的 path/status 记进 A 的行）
	// 会直接打破「每路径恰好 N 行」的计数
	paths := map[string]int{"/api/a": 200, "/api/b": 400, "/api/c": 404, "/api/d": 500}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Audit(db))
	for p, s := range paths {
		s := s
		r.POST(p, func(c *gin.Context) { c.Status(s) })
	}

	const rounds = 100
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		for p := range paths {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{"i":1}`)))
			}(p)
		}
	}
	wg.Wait()

	// 等异步审计全部落库
	wantTotal := int64(rounds * len(paths))
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int64
		db.Raw("SELECT COUNT(*) FROM audit_logs").Scan(&n)
		if n >= wantTotal || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	type row struct {
		Path   string
		Status int
		Method string
	}
	var rows []row
	if err := db.Raw("SELECT path, status, method FROM audit_logs").Scan(&rows).Error; err != nil {
		t.Fatalf("读审计行失败: %v", err)
	}
	if int64(len(rows)) != wantTotal {
		t.Fatalf("审计行数不符：got %d want %d", len(rows), wantTotal)
	}
	perPath := map[string]int{}
	for _, rw := range rows {
		if rw.Method != http.MethodPost {
			t.Fatalf("审计串台：path=%s 记成了 method=%s", rw.Path, rw.Method)
		}
		if s, ok := paths[rw.Path]; !ok || s != rw.Status {
			t.Fatalf("审计串台：path=%s status=%d 不是该路径注册的状态", rw.Path, rw.Status)
		}
		perPath[rw.Path]++
	}
	for p := range paths {
		if perPath[p] != rounds {
			t.Fatalf("路径 %s 审计计数串台：got %d want %d", p, perPath[p], rounds)
		}
	}
}
