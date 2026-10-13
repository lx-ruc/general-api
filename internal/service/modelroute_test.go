package service

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 模型启用态联动上游可用性（SyncModelRoutableStatus）：
// Key 全失效 → 自动停用；换回 Key → 自动恢复；手动停用永不自动恢复。

func newSyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/sync.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db
}

func seedModel(t *testing.T, h *gorm.DB, name string, status int, autoAt int64) {
	t.Helper()
	now := time.Now().Unix()
	if err := h.Exec(`INSERT INTO models (name, status, auto_disabled_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, name, status, autoAt, now, now).Error; err != nil {
		t.Fatal(err)
	}
}

// seedChannel 建渠道（可选 legacy 单 Key）+ 池 Key（可选启用）+ 模型能力
func seedChannel(t *testing.T, h *gorm.DB, id int64, name, model string, legacyKey string, poolKeyStatus int) {
	t.Helper()
	now := time.Now().Unix()
	if err := h.Exec(`INSERT INTO channels (id, name, base_url, path, upstream_key_enc, status, created_at, updated_at)
		VALUES (?, ?, 'https://up.example', '/v1/chat/completions', ?, 1, ?, ?)`, id, name, legacyKey, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if poolKeyStatus >= 0 { // -1 = 不建池 Key
		if err := h.Exec(`INSERT INTO channel_keys (channel_id, key_enc, status, created_at, updated_at)
			VALUES (?, 'k', ?, ?, ?)`, id, poolKeyStatus, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Exec(`INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, ?)`, id, model).Error; err != nil {
		t.Fatal(err)
	}
}

func modelState(t *testing.T, h *gorm.DB, name string) (status int, autoAt int64) {
	t.Helper()
	var row struct{ Status, AutoAt int64 }
	if err := h.Raw(`SELECT status AS status, auto_disabled_at AS auto_at FROM models WHERE name = ?`, name).Scan(&row).Error; err != nil {
		t.Fatalf("读取模型 %s 状态失败: %v", name, err)
	}
	return int(row.Status), row.AutoAt
}

// T1 Key 全禁用 → 自动停用；重新启用 Key → 自动恢复
func TestSyncModelRoutableStatusDisableAndRecover(t *testing.T) {
	h := newSyncDB(t)
	seedModel(t, h, "m1", 1, 0)
	seedChannel(t, h, 1, "ch1", "m1", "", 1) // 池内一把启用 Key

	disabled, _, err := SyncModelRoutableStatus(h)
	if err != nil || len(disabled) != 0 {
		t.Fatalf("有可用 Key 时不应停用: %v %v", disabled, err)
	}

	// Key 被上游 401 自动禁用 → 模型随之自动停用
	if err := h.Exec(`UPDATE channel_keys SET status = 0`).Error; err != nil {
		t.Fatal(err)
	}
	disabled, _, err = SyncModelRoutableStatus(h)
	if err != nil || len(disabled) != 1 || disabled[0] != "m1" {
		t.Fatalf("Key 全禁用应自动停用 m1: %v %v", disabled, err)
	}
	if s, at := modelState(t, h, "m1"); s != 0 || at <= 0 {
		t.Fatalf("应 status=0 且 auto_disabled_at>0, 得 status=%d auto=%d", s, at)
	}

	// 管理员换回 Key（重新启用）→ 模型自动恢复，标记清零
	if err := h.Exec(`UPDATE channel_keys SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	_, recovered, err := SyncModelRoutableStatus(h)
	if err != nil || len(recovered) != 1 || recovered[0] != "m1" {
		t.Fatalf("Key 恢复后 m1 应自动恢复: %v %v", recovered, err)
	}
	if s, at := modelState(t, h, "m1"); s != 1 || at != 0 {
		t.Fatalf("应 status=1 且 auto_disabled_at=0, 得 status=%d auto=%d", s, at)
	}

	// 再跑一次：状态已对齐，零变更（幂等）
	disabled, recovered, err = SyncModelRoutableStatus(h)
	if err != nil || len(disabled) != 0 || len(recovered) != 0 {
		t.Fatalf("对齐后再跑应零变更: disabled=%v recovered=%v err=%v", disabled, recovered, err)
	}
}

// T2 手动停用的模型（auto_disabled_at=0）即使上游恢复也不自动复活——管理员意图优先
func TestSyncModelRoutableStatusSkipsManualDisable(t *testing.T) {
	h := newSyncDB(t)
	seedModel(t, h, "m1", 0, 0)
	seedChannel(t, h, 1, "ch1", "m1", "", 1)

	_, recovered, err := SyncModelRoutableStatus(h)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("手动停用不应被自动恢复: %v %v", recovered, err)
	}
	if s, _ := modelState(t, h, "m1"); s != 0 {
		t.Fatalf("手动停用应保持 status=0, 得 %d", s)
	}
}

// T3 多渠道模型：一条渠道 Key 死了但另一条活着 → 不停用；全死才停
func TestSyncModelRoutableStatusMultiChannel(t *testing.T) {
	h := newSyncDB(t)
	seedModel(t, h, "m1", 1, 0)
	seedChannel(t, h, 1, "ch1", "m1", "", 1)
	seedChannel(t, h, 2, "ch2", "m1", "", 1)

	// 渠道 1 的 Key 禁用：渠道 2 还能承接 → 模型保持启用
	if err := h.Exec(`UPDATE channel_keys SET status = 0 WHERE channel_id = 1`).Error; err != nil {
		t.Fatal(err)
	}
	disabled, _, _ := SyncModelRoutableStatus(h)
	if len(disabled) != 0 {
		t.Fatalf("仍有存活渠道时不应停用: %v", disabled)
	}
	// 渠道 2 也死 → 停用
	if err := h.Exec(`UPDATE channel_keys SET status = 0 WHERE channel_id = 2`).Error; err != nil {
		t.Fatal(err)
	}
	disabled, _, _ = SyncModelRoutableStatus(h)
	if len(disabled) != 1 || disabled[0] != "m1" {
		t.Fatalf("全部渠道不可用应停用: %v", disabled)
	}
	// 任一渠道换回 Key → 恢复
	if err := h.Exec(`UPDATE channel_keys SET status = 1 WHERE channel_id = 2`).Error; err != nil {
		t.Fatal(err)
	}
	_, recovered, _ := SyncModelRoutableStatus(h)
	if len(recovered) != 1 {
		t.Fatalf("单渠道恢复即应恢复模型: %v", recovered)
	}
}

// T4 legacy 单 Key（无池）与渠道停用同样参与判定
func TestSyncModelRoutableStatusLegacyAndChannelStatus(t *testing.T) {
	h := newSyncDB(t)
	seedModel(t, h, "legacy-m", 1, 0)
	seedChannel(t, h, 1, "ch-legacy", "legacy-m", "enc-legacy", -1)
	seedModel(t, h, "chan-m", 1, 0)
	seedChannel(t, h, 2, "ch2", "chan-m", "", 1)

	// legacy 密文非空 = 可用
	if disabled, _, _ := SyncModelRoutableStatus(h); len(disabled) != 0 {
		t.Fatalf("legacy 单 Key 渠道应视为可用: %v", disabled)
	}
	// 渠道整体停用 → 两个模型都失去上游
	if err := h.Exec(`UPDATE channels SET status = 0`).Error; err != nil {
		t.Fatal(err)
	}
	disabled, _, _ := SyncModelRoutableStatus(h)
	if len(disabled) != 2 {
		t.Fatalf("渠道全停应停用两个模型: %v", disabled)
	}
}
