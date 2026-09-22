package config

import (
	"os"
	"path/filepath"
	"testing"
)

// LogDesc：postgres 模式必须脱敏密码（DSN 会进启动日志），且不得误打 sqlite 的
// Path 默认值；sqlite 模式原样给 driver:path
func TestDatabaseLogDesc(t *testing.T) {
	cases := []struct {
		name string
		db   Database
		want string
	}{
		{
			name: "sqlite 原样",
			db:   Database{Driver: "sqlite", Path: "data/token_.db"},
			want: "sqlite:data/token_.db",
		},
		{
			name: "postgres 密码脱敏",
			db:   Database{Driver: "postgres", DSN: "host=127.0.0.1 user=tg password=s3cret dbname=token_gateway port=5433 sslmode=disable"},
			want: "postgres:host=127.0.0.1 user=tg password=*** dbname=token_gateway port=5433 sslmode=disable",
		},
		{
			name: "postgres 空 DSN",
			db:   Database{Driver: "postgres", Path: "data/token_.db", DSN: ""},
			want: "postgres:",
		},
		{
			name: "postgres 非标准 DSN 不泄漏",
			db:   Database{Driver: "postgres", DSN: "postgres://tg:s3cret@localhost:5433/tg"},
			want: "postgres:(非标准 DSN，已隐藏)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.db.LogDesc()
			if got != tc.want {
				t.Fatalf("LogDesc = %q, want %q", got, tc.want)
			}
		})
	}
}

// jwt_secret 占位符守卫：照抄 config.example.yaml（占位符非空）不得静默启动——
// 公开仓库里的已知密钥等于任何人可伪造管理员 JWT（CLAUDE.md 承诺「不改启动报错」，
// 与 bootstrap 管理员密码占位符守卫 bootstrap.go 对称）
func TestLoadRejectsPlaceholderJWTSecret(t *testing.T) {
	dir := t.TempDir()
	writeCfg := func(secret string) string {
		p := filepath.Join(dir, "config.yaml")
		body := "security:\n  jwt_secret: " + secret + "\n"
		if secret == "" {
			body = "security:\n  jwt_secret: \"\"\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("TG_JWT_SECRET", "")

	if _, err := Load(writeCfg("change-me-to-a-random-string")); err == nil {
		t.Fatal("照抄 example 的占位 jwt_secret 应拒绝启动")
	}
	if _, err := Load(writeCfg("a-real-secret-xyz")); err != nil {
		t.Fatalf("真实密钥应通过: %v", err)
	}
	// 环境变量可救：占位符文件 + TG_JWT_SECRET 覆盖 → 放行（容器部署路径）
	t.Setenv("TG_JWT_SECRET", "from-env-secret")
	if _, err := Load(writeCfg("change-me-to-a-random-string")); err != nil {
		t.Fatalf("env 覆盖后应放行: %v", err)
	}
}
