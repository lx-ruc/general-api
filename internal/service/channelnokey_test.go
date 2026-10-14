package service

import (
	"strings"
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 渠道状态面收口（SyncChannelKeyState）：启用中但已无可用 Key 的渠道联动停用；
// 补 Key 后立即自动恢复。「无密钥不能启用」此前只拦动作，Key 流失后渠道以启用态悬挂。
// 与熔断/体检停用分工：无 Key 停用写 nokey_disabled_at（补 Key 即恢复），
// 熔断/体检写 auto_disabled_at（AutoProbeLoop 探测恢复），两个标记互不干扰。

// 启用中渠道的池 Key 全禁用 → SyncModelsAndNotify 联动停用渠道（写 nokey 标记与备注，
// 不写 auto_disabled_at——无 Key 探测必失败，不走探测恢复通道）
func TestSyncChannelNoKeyDisableOnLastKeyDead(t *testing.T) {
	db := newSyncDB(t)
	seedModel(t, db, "m1", 1, 0)
	seedChannel(t, db, 7, "ch7", "m1", "", 0) // 池里唯一 Key 已禁用

	SyncModelsAndNotify(db, "测试：Key 全禁用")

	var st struct {
		Status  int
		AutoAt  int64
		NoKeyAt int64
		Remark  string
	}
	if err := db.Raw("SELECT status, auto_disabled_at AS auto_at, nokey_disabled_at AS no_key_at, remark FROM channels WHERE id = 7").Scan(&st).Error; err != nil {
		t.Fatal(err)
	}
	if st.Status != 0 {
		t.Fatalf("渠道应联动停用，得 status=%d", st.Status)
	}
	if st.NoKeyAt == 0 {
		t.Fatal("应写 nokey_disabled_at（补 Key 自动恢复通道），得 0")
	}
	if st.AutoAt != 0 {
		t.Fatalf("不应写 auto_disabled_at（那是熔断/体检的探测恢复通道），得 %d", st.AutoAt)
	}
	if !strings.Contains(st.Remark, "无可用密钥") {
		t.Fatalf("备注应含原因，得 %q", st.Remark)
	}
}

// 有可用 Key / 仅 legacy 密文 / 已手动停用的渠道：均不动
func TestSyncChannelNoKeyDisableUntouched(t *testing.T) {
	db := newSyncDB(t)
	seedModel(t, db, "m2", 1, 0)
	seedChannel(t, db, 11, "ch-live", "m2", "", 1)       // 池 Key 启用
	seedChannel(t, db, 12, "ch-legacy", "m2", "enc", -1) // 仅 legacy 密文
	// 手动停用且无 Key 的渠道：保持原态（不被恢复、备注不被覆盖）
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, path, upstream_key_enc, status, auto_disabled_at, nokey_disabled_at, remark, created_at, updated_at)
		VALUES (13, 'ch-manual', 'https://up.example', '/v1/chat/completions', '', 0, 0, 0, '管理员停用', ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	SyncModelsAndNotify(db, "测试：不应误伤")

	var statuses []struct {
		ID      int64
		Status  int
		AutoAt  int64
		NoKeyAt int64
		Remark  string
	}
	if err := db.Raw("SELECT id, status, auto_disabled_at AS auto_at, nokey_disabled_at AS no_key_at, remark FROM channels ORDER BY id").Scan(&statuses).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		switch s.ID {
		case 11, 12:
			if s.Status != 1 || s.AutoAt != 0 || s.NoKeyAt != 0 {
				t.Fatalf("有可用 Key 的渠道不应被动: %+v", s)
			}
		case 13:
			if s.Status != 0 || s.AutoAt != 0 || s.NoKeyAt != 0 || s.Remark != "管理员停用" {
				t.Fatalf("手动停用渠道应保持原态: %+v", s)
			}
		}
	}
}

// 对称恢复：无 Key 停用的渠道补回池 Key → SyncModelsAndNotify 一次调用内
// 渠道自动启用（status=1、清 nokey 标记、写恢复备注）+ 独家模型随之恢复
func TestSyncChannelKeyStateRecoverOnKeyRestore(t *testing.T) {
	db := newSyncDB(t)
	seedModel(t, db, "m3", 1, 0)
	seedChannel(t, db, 8, "ch8", "m3", "", 0) // 池 Key 全禁用
	SyncModelsAndNotify(db, "测试：Key 全禁用")

	// 补回可用 Key → 渠道与模型一次调用同步恢复
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO channel_keys (channel_id, key_enc, status, created_at, updated_at)
		VALUES (8, 'newkey', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	SyncModelsAndNotify(db, "测试：补回 Key")

	var ch struct {
		Status  int
		NoKeyAt int64
		Remark  string
	}
	if err := db.Raw("SELECT status, nokey_disabled_at AS no_key_at, remark FROM channels WHERE id = 8").Scan(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Status != 1 || ch.NoKeyAt != 0 {
		t.Fatalf("补 Key 后渠道应自动启用并清标记，得 status=%d nokey_at=%d", ch.Status, ch.NoKeyAt)
	}
	if !strings.Contains(ch.Remark, "自动恢复") {
		t.Fatalf("备注应写恢复记录，得 %q", ch.Remark)
	}
	if status, autoAt := modelState(t, db, "m3"); status != 1 || autoAt != 0 {
		t.Fatalf("独家模型应随渠道恢复，得 status=%d auto_at=%d", status, autoAt)
	}
}

// 分工守卫：熔断/体检停用（auto_disabled_at>0、无 nokey 标记）的渠道即使有可用 Key
// 也不被本收口恢复——那类渠道只经 AutoProbeLoop 探测恢复，否则熔断形同虚设
func TestSyncChannelKeyStateSkipsBreakerDisabled(t *testing.T) {
	db := newSyncDB(t)
	now := time.Now().Unix()
	// 熔断停用 + 池 Key 依然启用（典型：上游 5xx 连续失败被熔断）
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, path, status, auto_disabled_at, remark, created_at, updated_at)
		VALUES (9, 'ch-breaker', 'https://up.example', '/v1/chat/completions', 0, ?, '熔断停用', ?, ?)`, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO channel_keys (channel_id, key_enc, status, created_at, updated_at)
		VALUES (9, 'k', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	SyncModelsAndNotify(db, "测试：熔断渠道不应恢复")

	var ch struct {
		Status int
		AutoAt int64
	}
	if err := db.Raw("SELECT status, auto_disabled_at AS auto_at FROM channels WHERE id = 9").Scan(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Status != 0 || ch.AutoAt == 0 {
		t.Fatalf("熔断停用的渠道不应被补 Key 路径恢复: status=%d auto_at=%d", ch.Status, ch.AutoAt)
	}
}

// 直接调用（启动清扫入口）：停用后幂等，重复执行不重复写
func TestSyncChannelKeyStateIdempotent(t *testing.T) {
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/sweep.db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO channels (id, name, base_url, path, upstream_key_enc, status, created_at, updated_at)
		VALUES (21, 'ch-sweep', 'https://up.example', '/v1/chat/completions', '', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	if disabled, recovered := SyncChannelKeyState(db, "启动清扫"); len(disabled) != 1 || disabled[0] != "ch-sweep" || len(recovered) != 0 {
		t.Fatalf("应停用并返回渠道名，得 disabled=%v recovered=%v", disabled, recovered)
	}
	if disabled, recovered := SyncChannelKeyState(db, "启动清扫"); len(disabled) != 0 || len(recovered) != 0 {
		t.Fatalf("重复执行应无动作，得 disabled=%v recovered=%v", disabled, recovered)
	}
	var status int
	_ = db.Raw("SELECT status FROM channels WHERE id = 21").Scan(&status).Error
	if status != 0 {
		t.Fatalf("渠道应为停用态，得 %d", status)
	}
}
