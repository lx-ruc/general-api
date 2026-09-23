package database

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"token-gateway/internal/config"
)

// 复刻生产实测的存量脏数据形态：无密钥渠道挂模型能力、从未/不再有渠道支撑的孤儿模型与授权
func openCleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(config.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func insertChannel(t *testing.T, db *gorm.DB, id int, name, keyEnc string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, upstream_key_enc, created_at, updated_at)
		VALUES (?, ?, 'https://up.example', ?, 0, 0)`, id, name, keyEnc).Error; err != nil {
		t.Fatalf("insert channel %s: %v", name, err)
	}
}

func insertKey(t *testing.T, db *gorm.DB, channelID, status int) {
	t.Helper()
	if err := db.Exec(`INSERT INTO channel_keys (channel_id, key_enc, status, created_at, updated_at)
		VALUES (?, 'enc', ?, 0, 0)`, channelID, status).Error; err != nil {
		t.Fatalf("insert key ch%d: %v", channelID, err)
	}
}

func insertAbility(t *testing.T, db *gorm.DB, channelID int, model string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, ?)`,
		channelID, model).Error; err != nil {
		t.Fatalf("insert ability ch%d %s: %v", channelID, model, err)
	}
}

func insertModel(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO models (name, created_at, updated_at) VALUES (?, 0, 0)`,
		name).Error; err != nil {
		t.Fatalf("insert model %s: %v", name, err)
	}
}

func abilityCount(t *testing.T, db *gorm.DB, channelID int) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM channel_abilities WHERE channel_id = ?`, channelID).Scan(&n).Error; err != nil {
		t.Fatalf("count abilities ch%d: %v", channelID, err)
	}
	return n
}

func modelExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM models WHERE name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatalf("count model %s: %v", name, err)
	}
	return n > 0
}

func grantModels(t *testing.T, db *gorm.DB, userID int) []string {
	t.Helper()
	var names []string
	if err := db.Raw(`SELECT model_name FROM user_model_grants WHERE user_id = ? ORDER BY model_name`,
		userID).Scan(&names).Error; err != nil {
		t.Fatalf("list grants user%d: %v", userID, err)
	}
	return names
}

func TestCleanupKeylessChannelsAndOrphanModels(t *testing.T) {
	db := openCleanupDB(t)
	// 渠道矩阵：c1 无任何密钥 / c2 池内启用密钥 / c3 仅 legacy 密文 / c4 只有 1 把禁用池密钥
	insertChannel(t, db, 1, "c1-无密钥", "")
	insertChannel(t, db, 2, "c2-池密钥", "")
	insertChannel(t, db, 3, "c3-legacy", "enc-legacy")
	insertChannel(t, db, 4, "c4-禁用池密钥", "")
	insertKey(t, db, 2, 1)
	insertKey(t, db, 4, 0)
	insertAbility(t, db, 1, "model-a")
	insertAbility(t, db, 1, "model-b")
	insertAbility(t, db, 2, "model-c")
	insertAbility(t, db, 3, "model-e")
	insertAbility(t, db, 4, "model-f")
	// model-a 仅由无密钥渠道支撑（清理后成孤儿）；model-x 从无任何渠道支撑
	for _, m := range []string{"model-a", "model-c", "model-e", "model-f", "model-x"} {
		insertModel(t, db, m)
	}
	if err := db.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES (1, 'u1', 'x', 'member', 0, 0), (2, 'u2', 'x', 'member', 0, 0)`).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if err := db.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES
		(1, 'model-a', 0), (1, 'model-c', 0), (2, 'model-x', 0)`).Error; err != nil {
		t.Fatalf("insert grants: %v", err)
	}

	if err := Cleanup(db); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// 无密钥判定看 Key 池全部行（不看 status）且 legacy 密文为空：仅 c1 被置空
	if n := abilityCount(t, db, 1); n != 0 {
		t.Errorf("无密钥渠道 c1 的模型能力应被置空，剩 %d 条", n)
	}
	for _, c := range []struct{ id int; want int64 }{{2, 1}, {3, 1}, {4, 1}} {
		if n := abilityCount(t, db, c.id); n != c.want {
			t.Errorf("渠道 %d 的模型能力应保留 %d 条，实际 %d 条", c.id, c.want, n)
		}
	}
	// 孤儿模型连同授权删除；有渠道支撑的保留
	for _, m := range []string{"model-a", "model-x"} {
		if modelExists(t, db, m) {
			t.Errorf("无渠道支撑的模型 %s 应被删除", m)
		}
	}
	for _, m := range []string{"model-c", "model-e", "model-f"} {
		if !modelExists(t, db, m) {
			t.Errorf("有渠道支撑的模型 %s 应保留", m)
		}
	}
	if got := grantModels(t, db, 1); len(got) != 1 || got[0] != "model-c" {
		t.Errorf("u1 授权应只剩 model-c，实际 %v", got)
	}
	if got := grantModels(t, db, 2); len(got) != 0 {
		t.Errorf("u2 的孤儿模型授权应被删除，实际 %v", got)
	}

	// 幂等：二跑不报错、各表计数不变
	if err := Cleanup(db); err != nil {
		t.Fatalf("cleanup 二跑: %v", err)
	}
	if n := abilityCount(t, db, 1); n != 0 {
		t.Errorf("二跑后 c1 应仍为空，实际 %d 条", n)
	}
	var mc, mm, mg int64
	_ = db.Raw("SELECT COUNT(*) FROM channel_abilities").Scan(&mc).Error
	_ = db.Raw("SELECT COUNT(*) FROM models").Scan(&mm).Error
	_ = db.Raw("SELECT COUNT(*) FROM user_model_grants").Scan(&mg).Error
	if mc != 3 || mm != 3 || mg != 1 {
		t.Errorf("二跑后计数应稳定 abilities=3 models=3 grants=1，实际 %d/%d/%d", mc, mm, mg)
	}
}
