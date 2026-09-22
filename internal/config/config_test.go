package config

import "testing"

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
