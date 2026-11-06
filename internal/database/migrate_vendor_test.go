package database

import (
	"strconv"
	"testing"

	"gorm.io/gorm"

	"token-gateway/internal/config"
)

// 厂商实体化存量迁移：建实体/回填归属/拆分双厂商模型/授权折算，全部幂等。
// 直接在新库上造「存量数据形态」（channels 带标签、vendor_id 列默认 0），
// 再跑 Migrate 触发迁移块（首次 Migrate 时无数据，迁移块 no-op）
func newVendorMigrateDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/t.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	if err := Migrate(gdb); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 模拟未迁移存量库：手工补回 vendor 文本列与 channel_keys 表（drops 已在批二移除）
	mustExecVendor(t, gdb, `ALTER TABLE channels ADD COLUMN vendor TEXT NOT NULL DEFAULT ''`)
	mustExecVendor(t, gdb, `ALTER TABLE models ADD COLUMN vendor TEXT NOT NULL DEFAULT ''`)
	mustExecVendor(t, gdb, `CREATE TABLE IF NOT EXISTS channel_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT, channel_id INTEGER NOT NULL,
		key_enc TEXT NOT NULL, weight INTEGER NOT NULL DEFAULT 1,
		status INTEGER NOT NULL DEFAULT 1, remark TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`)
	mustExecVendor(t, gdb, `ALTER TABLE channels ADD COLUMN upstream_key_enc TEXT NOT NULL DEFAULT ''`)
	return gdb
}

func vendorIDOf(t *testing.T, gdb *gorm.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := gdb.Raw("SELECT id FROM vendors WHERE name = ?", name).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("厂商 %s 应存在: %v %d", name, err, id)
	}
	return id
}

// 全链路：标签建实体、空标签归未分类、双厂商模型拆分（能力挪移+授权/价格复制+原名归
// volces/Thu）、vendor_id 回填、授权折算去重；二次 Migrate 幂等（行数不变）
func TestMigrateVendorsFull(t *testing.T) {
	gdb := newVendorMigrateDB(t)
	now := int64(1730000000)
	// 存量形态：三个标签渠道（Thu/berry 双承接 261017，volces 承接其他）、一个空标签
	mustExecVendor(t, gdb, `INSERT INTO channels (id,name,vendor,base_url,path,weight,priority,status,created_at,updated_at)
		VALUES (1,'ch-thu','volces/Thu','https://u.example','/chat/completions',1,10,1,?,?),
		       (2,'ch-berry','volces/berry','https://u.example','/chat/completions',1,10,1,?,?),
		       (3,'ch-volces','volces','https://u.example','/chat/completions',1,5,1,?,?),
		       (4,'ch-none','','https://u.example','/chat/completions',1,1,1,?,?)`, now, now, now, now, now, now, now, now)
	mustExecVendor(t, gdb, `INSERT INTO models (name,vendor,input_price,output_price,status,type,created_at,updated_at)
		VALUES ('deepseek-v4-1-flash-261017','volces/Thu',1000000,2000000,1,'chat',?,?),
		       ('glm-5-2-260617','volces',3000000,8000000,1,'chat',?,?)`, now, now, now, now)
	mustExecVendor(t, gdb, `INSERT INTO channel_abilities (channel_id, model_name) VALUES
		(1,'deepseek-v4-1-flash-261017'), (2,'deepseek-v4-1-flash-261017'), (3,'glm-5-2-260617'),
		(4,'glm-5-2-260617')`) // glm 同时被空标签渠道承接（priority 低，④ 应取 volces）
	mustExecVendor(t, gdb, `INSERT INTO orgs (id,name,quota_limit,status,created_at,updated_at) VALUES (1,'o1',0,1,?,?)`, now, now)
	mustExecVendor(t, gdb, `INSERT INTO users (id,org_id,username,password_hash,role,status,created_at,updated_at)
		VALUES (1,1,'u1','x','member',1,?,?)`, now, now)
	// 同一客户两个模型都来自 volces → 折算应去重为一行
	mustExecVendor(t, gdb, `INSERT INTO org_model_grants (org_id, model_name, created_at)
		VALUES (1,'glm-5-2-260617',?), (1,'deepseek-v4-1-flash-261017',?)`, now, now)
	mustExecVendor(t, gdb, `INSERT INTO user_model_grants (user_id, model_name, created_at)
		VALUES (1,'glm-5-2-260617',?), (1,'deepseek-v4-1-flash-261017',?)`, now, now)
	mustExecVendor(t, gdb, `INSERT INTO org_model_prices (org_id, model_name, input_price, output_price, created_at, updated_at)
		VALUES (1,'deepseek-v4-1-flash-261017',500000,900000,?,?)`, now, now)

	if err := Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// ① 厂商实体
	if got := vendorIDOf(t, gdb, "volces/Thu"); got == 0 {
		t.Fatal("volces/Thu 应建实体")
	}
	vendorIDOf(t, gdb, "volces/berry")
	vendorIDOf(t, gdb, "volces")
	vendorIDOf(t, gdb, "uncategorized")

	// ② 渠道归属
	var chNoneVID int64
	_ = gdb.Raw("SELECT vendor_id FROM channels WHERE id = 4").Scan(&chNoneVID).Error
	if chNoneVID != vendorIDOf(t, gdb, "uncategorized") {
		t.Fatalf("空标签渠道应归未分类，got vendor_id=%d", chNoneVID)
	}

	// ③ 不拆分：同名模型跨厂商承接——原名能力同时挂在 Thu 与 berry 渠道，无 -berry 副本
	var srcAbCh []int64
	_ = gdb.Raw("SELECT channel_id FROM channel_abilities WHERE model_name = 'deepseek-v4-1-flash-261017' ORDER BY channel_id").Scan(&srcAbCh).Error
	if len(srcAbCh) != 2 || srcAbCh[0] != 1 || srcAbCh[1] != 2 {
		t.Fatalf("原名能力应同时挂 Thu(1) 与 berry(2) 渠道，got %v", srcAbCh)
	}
	var copyCnt int64
	_ = gdb.Raw("SELECT COUNT(*) FROM models WHERE name = 'deepseek-v4-1-flash-261017-berry'").Scan(&copyCnt).Error
	if copyCnt != 0 {
		t.Fatal("不应存在 -berry 副本（跨厂商承接政策）")
	}
	// 迁移后手动造一个副本（模拟批一已拆分的库）再跑迁移 → 合并回原名
	mustExecVendor(t, gdb, `INSERT INTO models (name, vendor_id, status, created_at, updated_at)
		VALUES ('deepseek-v4-1-flash-261017-berry', `+itoa(vendorIDOf(t, gdb, "volces/berry"))+`, 1, 0, 0)`)
	mustExecVendor(t, gdb, `UPDATE channel_abilities SET model_name = 'deepseek-v4-1-flash-261017-berry' WHERE channel_id = 2`)
	mustExecVendor(t, gdb, `INSERT INTO org_model_prices (org_id, model_name, input_price, output_price, created_at, updated_at)
		VALUES (1, 'deepseek-v4-1-flash-261017-berry', 700, 800, 0, 0)`)
	if err := Migrate(gdb); err != nil {
		t.Fatalf("合并迁移失败: %v", err)
	}
	var mergedCnt int64
	_ = gdb.Raw("SELECT COUNT(*) FROM models WHERE name = 'deepseek-v4-1-flash-261017-berry'").Scan(&mergedCnt).Error
	if mergedCnt != 0 {
		t.Fatal("合并迁移应删除 -berry 副本")
	}
	var backAb []int64
	_ = gdb.Raw("SELECT channel_id FROM channel_abilities WHERE model_name = 'deepseek-v4-1-flash-261017' ORDER BY channel_id").Scan(&backAb).Error
	if len(backAb) != 2 {
		t.Fatalf("合并后原名能力应挂两渠道，got %v", backAb)
	}
	var mergedPrice int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM org_model_prices WHERE model_name = 'deepseek-v4-1-flash-261017' AND org_id = 1`).Scan(&mergedPrice).Error
	if mergedPrice != 1 {
		t.Fatalf("副本覆盖价应并回原名（原名已有的保留），got %d 行", mergedPrice)
	}

	// ④ glm 归属按优先级取 volces（priority 5 > 空标签渠道 1）
	var glmVID int64
	_ = gdb.Raw("SELECT vendor_id FROM models WHERE name = 'glm-5-2-260617'").Scan(&glmVID).Error
	if glmVID != vendorIDOf(t, gdb, "volces") {
		t.Fatalf("glm 应归 volces（按渠道优先级），got %d", glmVID)
	}

	// ⑤ 折算去重（按主厂商）：glm→volces + 261017→Thu = 两行（261017 跨厂商承接，
	// Thu 已授权即可用，berry 无需单独授权行）
	var orgVG int64
	_ = gdb.Raw("SELECT COUNT(*) FROM org_vendor_grants WHERE org_id = 1").Scan(&orgVG).Error
	if orgVG != 2 {
		t.Fatalf("客户厂商授权应为 2 行（volces/Thu），got %d", orgVG)
	}
	var userVG int64
	_ = gdb.Raw("SELECT COUNT(*) FROM user_vendor_grants WHERE user_id = 1").Scan(&userVG).Error
	if userVG != 2 {
		t.Fatalf("子账号厂商授权应为 2 行，got %d", userVG)
	}

	// 幂等：再跑两遍，全部行数不变
	if err := Migrate(gdb); err != nil || Migrate(gdb) != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM vendors`,
		`SELECT COUNT(*) FROM models WHERE name LIKE 'deepseek-v4-1-flash-261017%'`,
		`SELECT COUNT(*) FROM org_vendor_grants`,
		`SELECT COUNT(*) FROM user_vendor_grants`,
		`SELECT COUNT(*) FROM org_model_grants WHERE model_name = 'deepseek-v4-1-flash-261017-berry'`,
	} {
		var n int64
		_ = gdb.Raw(q).Scan(&n).Error
		var n2 int64
		_ = gdb.Raw(q).Scan(&n2).Error
		if n != n2 {
			t.Fatalf("迁移不幂等: %s", q)
		}
	}
}

// 孤儿模型（无任何承接渠道）不参与归属回填与授权折算
func TestMigrateVendorsOrphanModel(t *testing.T) {
	gdb := newVendorMigrateDB(t)
	now := int64(1730000000)
	mustExecVendor(t, gdb, `INSERT INTO channels (id,name,vendor,base_url,path,status,created_at,updated_at)
		VALUES (1,'ch','volces','https://u.example','/chat/completions',1,?,?)`, now, now)
	mustExecVendor(t, gdb, `INSERT INTO models (name,vendor,status,created_at,updated_at)
		VALUES ('lonely','',1,?,?)`, now, now) // 无 abilities
	mustExecVendor(t, gdb, `INSERT INTO orgs (id,name,quota_limit,status,created_at,updated_at) VALUES (1,'o1',0,1,?,?)`, now, now)
	mustExecVendor(t, gdb, `INSERT INTO org_model_grants (org_id, model_name, created_at) VALUES (1,'lonely',?)`, now)

	if err := Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	var vid int64
	_ = gdb.Raw("SELECT vendor_id FROM models WHERE name = 'lonely'").Scan(&vid).Error
	if vid != 0 {
		t.Fatalf("孤儿模型 vendor_id 应保持 0，got %d", vid)
	}
	var vg int64
	_ = gdb.Raw("SELECT COUNT(*) FROM org_vendor_grants").Scan(&vg).Error
	if vg != 0 {
		t.Fatalf("孤儿模型不应折算出厂商授权，got %d", vg)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func mustExecVendor(t *testing.T, gdb *gorm.DB, q string, args ...any) {
	t.Helper()
	if err := gdb.Exec(q, args...).Error; err != nil {
		t.Fatalf("执行失败 %s: %v", q, err)
	}
}
