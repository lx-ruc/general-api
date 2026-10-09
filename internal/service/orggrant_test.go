package service

import (
	"fmt"
	"testing"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 存量继承（backfill）：首次升级把每个客户「其子账号已授权模型并集」写入
// org_model_grants；settings 标记保证只跑一次——手动回收后重启不能复活。

func setupOrgGrantDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/og.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	// org 1（两个成员授权 m1/m2/m3）、org 2（一个成员授权 m1）、platform 账号无 org
	stmts := []string{
		`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
			VALUES (1, 'a', 100000000, 1, unixepoch(), unixepoch()),
			       (2, 'b', 100000000, 1, unixepoch(), unixepoch())`,
		`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at) VALUES
			(1, 1,    'u1',   'x', 'member',         1, unixepoch(), unixepoch()),
			(2, 1,    'u2',   'x', 'member',         1, unixepoch(), unixepoch()),
			(3, 2,    'u3',   'x', 'member',         1, unixepoch(), unixepoch()),
			(4, NULL, 'root', 'x', 'platform_admin', 1, unixepoch(), unixepoch())`,
		`INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES
			(1, 'm1', unixepoch()), (1, 'm2', unixepoch()),
			(2, 'm3', unixepoch()), (3, 'm1', unixepoch())`,
	}
	for _, s := range stmts {
		if err := gdb.Exec(s).Error; err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	return gdb
}

func orgGrantRows(t *testing.T, gdb *gorm.DB) map[string][]string {
	t.Helper()
	type row struct {
		OrgID     int64
		ModelName string
	}
	var rows []row
	if err := gdb.Raw(`SELECT org_id, model_name FROM org_model_grants`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, r := range rows {
		key := fmt.Sprintf("%d", r.OrgID)
		out[key] = append(out[key], r.ModelName)
	}
	return out
}

// T1 首跑：并集继承 + source=backfill + settings 标记写入
func TestBackfillOrgModelGrants(t *testing.T) {
	gdb := setupOrgGrantDB(t)
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatalf("backfill 失败: %v", err)
	}
	rows := orgGrantRows(t, gdb)
	if len(rows["1"]) != 3 || len(rows["2"]) != 1 {
		t.Fatalf("并集继承错误: %+v", rows)
	}
	var src string
	_ = gdb.Raw(`SELECT source FROM org_model_grants WHERE org_id = 1 LIMIT 1`).Scan(&src).Error
	if src != "backfill" {
		t.Fatalf("source 应为 backfill，得 %q", src)
	}
	var flag string
	_ = gdb.Raw(`SELECT value FROM settings WHERE key = ?`, SettingOrgGrantsBackfilled).Scan(&flag).Error
	if flag == "" {
		t.Fatal("settings 标记未写入")
	}
}

// T2 幂等：标记存在时二次调用不再写入（回收后不会被重新继承，见 T3）
func TestBackfillOrgModelGrantsIdempotent(t *testing.T) {
	gdb := setupOrgGrantDB(t)
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatal(err)
	}
	// 新增一个成员授权（模拟回收后又给子账号授权前的中间态），再跑应无变化
	_ = gdb.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at)
		VALUES (3, 'm9', unixepoch())`).Error
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatal(err)
	}
	var cnt int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM org_model_grants WHERE model_name = 'm9'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatal("标记已存在时不应再继承新授权")
	}
}

// T3 手动回收（删 org grant + 级联删子账号授权）后重跑 backfill 不复活
func TestBackfillDoesNotReviveRecycled(t *testing.T) {
	gdb := setupOrgGrantDB(t)
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatal(err)
	}
	// 模拟 SetOrgModels 的回收路径：删开通行 + 级联删子账号授权
	if err := gdb.Exec(`DELETE FROM org_model_grants WHERE org_id = 1 AND model_name = 'm1'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`DELETE FROM user_model_grants WHERE model_name = 'm1'
		AND user_id IN (SELECT id FROM users WHERE org_id = 1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatal(err)
	}
	var cnt int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM org_model_grants WHERE org_id = 1 AND model_name = 'm1'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatal("已回收的开通不应在重启后复活")
	}
}

// T4 空授权：没有任何子账号授权时继承空集且标记照写（新客户/新装场景）
func TestBackfillEmpty(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/empty.db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	if err := BackfillOrgModelGrants(gdb); err != nil {
		t.Fatal(err)
	}
	var cnt int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM org_model_grants`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("空库应继承 0 行，得 %d", cnt)
	}
	var flag string
	_ = gdb.Raw(`SELECT value FROM settings WHERE key = ?`, SettingOrgGrantsBackfilled).Scan(&flag).Error
	if flag == "" {
		t.Fatal("空集也必须写标记（否则每次启动都重跑）")
	}
}
