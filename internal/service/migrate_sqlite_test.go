package service

import (
	"path/filepath"
	"strings"
	"testing"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 全链路：源 SQLite（含无 id 列的 settings 表）→ 新库，逐表行数一致且 settings 键值原样到达。
// 回归点：settings 以 key 为主键、无 id 列，读取时按 id 排序会直接报 no such column: id。
// 源路径不存在必须报错：SQLite 驱动会静默新建空库，打错字会变成"成功迁移 0 行"
func TestMigrateFromSQLiteMissingSource(t *testing.T) {
	dst, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/dst.db"})
	if err != nil {
		t.Fatalf("打开目标库失败: %v", err)
	}
	if err := database.Migrate(dst); err != nil {
		t.Fatalf("建目标库 schema 失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := dst.DB(); _ = sqlDB.Close() })
	if err := MigrateFromSQLite(dst, filepath.Join(t.TempDir(), "no-such.db")); err == nil {
		t.Fatal("源文件不存在时应报错，而非静默空迁移")
	}
}

func TestMigrateFromSQLite(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "src.db")
	src, err := database.Open(config.Database{Driver: "sqlite", Path: srcPath})
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	if err := database.Migrate(src); err != nil {
		t.Fatalf("建源库 schema 失败: %v", err)
	}
	seed := []string{
		`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at) VALUES (1, 'o1', 1000, 1, 0, 0)`,
		`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at) VALUES (1, 1, 'u1', 'h', 'member', 1, 0, 0)`,
		`INSERT INTO settings (key, value) VALUES ('points_per_yuan', '1000000')`,
		// 回归点：recharge_requests / audit_logs 曾漏在迁移清单外，
		// 切 PG 搬数据会静默丢充值审批与操作审计流水
		`INSERT INTO recharge_requests (id, org_id, amount, voucher, status, handled_by, handled_at, reply, created_at) VALUES (1, 1, 500, 'V1', 'approved', 1, 1, 'ok', 1)`,
		`INSERT INTO audit_logs (id, actor_id, actor, method, path, status, detail, ip, created_at) VALUES (1, 1, 'u1', 'POST', '/api/x', 200, 'd', '127.0.0.1', 1)`,
		// 回归点：org_model_* 与 notifications 也曾整体漏迁——丢客户差异化定价
		// （计费直接回落平台价）与模型开通白名单，靠漏表守卫防再犯
		`INSERT INTO org_model_grants (id, org_id, model_name, granted_by, source, created_at) VALUES (1, 1, 'm1', NULL, 'grant', 1)`,
		`INSERT INTO org_model_prices (id, org_id, model_name, input_price, output_price, created_at, updated_at) VALUES (1, 1, 'm1', 100, 200, 1, 1)`,
		`INSERT INTO org_model_requests (id, org_id, model_names, reason, status, created_at) VALUES (1, 1, 'm1', 'r', 'pending', 1)`,
		`INSERT INTO notifications (id, user_id, type, title, body, created_at) VALUES (1, 1, 't', '标题', '正文', 1)`,
	}
	for _, q := range seed {
		if err := src.Exec(q).Error; err != nil {
			t.Fatalf("造源数据失败: %v (%s)", err, q)
		}
	}
	if sqlDB, _ := src.DB(); sqlDB != nil {
		_ = sqlDB.Close()
	}

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := database.Open(config.Database{Driver: "sqlite", Path: dstPath})
	if err != nil {
		t.Fatalf("打开目标库失败: %v", err)
	}
	if err := database.Migrate(dst); err != nil {
		t.Fatalf("建目标库 schema 失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := dst.DB(); _ = sqlDB.Close() })

	if err := MigrateFromSQLite(dst, srcPath); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	for _, tc := range []struct{ table string; want int64 }{
		{"orgs", 1}, {"users", 1}, {"settings", 1},
		{"recharge_requests", 1}, {"audit_logs", 1},
		{"org_model_grants", 1}, {"org_model_prices", 1},
		{"org_model_requests", 1}, {"notifications", 1},
	} {
		var got int64
		if err := dst.Table(tc.table).Count(&got).Error; err != nil {
			t.Fatalf("统计 %s 失败: %v", tc.table, err)
		}
		if got != tc.want {
			t.Fatalf("表 %s 行数不符：want %d got %d", tc.table, tc.want, got)
		}
	}
	var v string
	if err := dst.Raw(`SELECT value FROM settings WHERE key = 'points_per_yuan'`).Scan(&v).Error; err != nil {
		t.Fatalf("回读 settings 失败: %v", err)
	}
	if v != "1000000" {
		t.Fatalf("settings 值不符: %q", v)
	}

	// 幂等防重：目标表非空时整表跳过，不报错也不重复导入
	if err := MigrateFromSQLite(dst, srcPath); err != nil {
		t.Fatalf("重复迁移应跳过而非报错: %v", err)
	}
	var n int64
	_ = dst.Table("users").Count(&n).Error
	if n != 1 {
		t.Fatalf("重复导入了数据: users=%d", n)
	}
}

// 漏表守卫：源库出现清单未覆盖的表（模拟 schema.sql 加了新表但迁移清单没同步）
// 必须硬失败并点名漏网表；瞬态白名单内的表（验证码等）即便有数据也不触发。
// 守卫先于复制执行：失败时目标库不应残留任何已搬数据。
func TestMigrateFromSQLiteUncoveredTableGuard(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "src.db")
	src, err := database.Open(config.Database{Driver: "sqlite", Path: srcPath})
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	if err := database.Migrate(src); err != nil {
		t.Fatalf("建源库 schema 失败: %v", err)
	}
	seed := []string{
		`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at) VALUES (1, 'o1', 1000, 1, 0, 0)`,
		`INSERT INTO verification_codes (email, code, expire_at, sent_at) VALUES ('a@b.c', '123456', 99, 1)`,
		`CREATE TABLE future_business (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)`,
		`INSERT INTO future_business (id, payload) VALUES (1, 'x')`,
	}
	for _, q := range seed {
		if err := src.Exec(q).Error; err != nil {
			t.Fatalf("造源数据失败: %v (%s)", err, q)
		}
	}
	if sqlDB, _ := src.DB(); sqlDB != nil {
		_ = sqlDB.Close()
	}

	dst, err := database.Open(config.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "dst.db")})
	if err != nil {
		t.Fatalf("打开目标库失败: %v", err)
	}
	if err := database.Migrate(dst); err != nil {
		t.Fatalf("建目标库 schema 失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := dst.DB(); _ = sqlDB.Close() })

	err = MigrateFromSQLite(dst, srcPath)
	if err == nil {
		t.Fatal("源库存在未覆盖表时应报错，而非静默丢表")
	}
	if !strings.Contains(err.Error(), "future_business") {
		t.Fatalf("错误信息应点名漏网表 future_business: %v", err)
	}
	if strings.Contains(err.Error(), "verification_codes") {
		t.Fatalf("瞬态白名单表不应被报为漏网: %v", err)
	}
	var n int64
	_ = dst.Table("orgs").Count(&n).Error
	if n != 0 {
		t.Fatalf("守卫应先于复制执行，目标库不应有数据: orgs=%d", n)
	}
}
