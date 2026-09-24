package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

func TestNotifyKeyQuotaCoolingFanOut(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	for _, row := range []struct {
		id     int64
		role   string
		status int
	}{
		{1, "platform_admin", 1},
		{2, "platform_admin", 1},
		{3, "platform_admin", 0}, // 停用的不应收到
		{4, "org_admin", 1},      // 客户管理员不应收到
	} {
		if err := gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
			VALUES (?, NULL, ?, 'x', ?, ?, ?, ?)`,
			row.id, "user"+strings.Repeat("a", int(row.id)), row.role, row.status, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}

	raw := "sk-abcdefghijklmnopqrstuvwx"
	if !NotifyKeyQuotaCooling(gdb, 7, 15, "火山通道", raw, "SetLimitExceeded") {
		t.Fatal("首次进入冷却应发出通知")
	}
	var rows []struct {
		ID      int64
		UserID  int64
		Type    string
		Title   string
		Payload string
		ReadAt  int64
	}
	if err := gdb.Raw("SELECT id, user_id, type, title, payload, read_at FROM notifications ORDER BY user_id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应只给 2 个启用的系统管理员各发一条，got %d", len(rows))
	}
	for i, want := range []int64{1, 2} {
		if rows[i].UserID != want || rows[i].Type != NotifyTypeKeyQuotaCooling || rows[i].ReadAt != 0 {
			t.Fatalf("第 %d 行应是给用户 %d 的未读 %s 通知，got %+v", i, want, NotifyTypeKeyQuotaCooling, rows[i])
		}
	}
	var p KeyQuotaPayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &p); err != nil {
		t.Fatalf("payload 应为合法 JSON: %v", err)
	}
	if p.ChannelID != 7 || p.KeyID != 15 || p.ChannelName != "火山通道" || p.ErrCode != "SetLimitExceeded" {
		t.Fatalf("payload 定位信息不符: %+v", p)
	}
	if p.KeyMasked != MaskKey(raw) || strings.Contains(rows[0].Payload, raw) {
		t.Fatalf("payload 只能放打码 Key（%q），不得出现明文", p.KeyMasked)
	}
	if !strings.Contains(rows[0].Title, "火山通道") {
		t.Fatalf("标题应含渠道名: %q", rows[0].Title)
	}
}

// 节流：同一渠道+Key 窗口内只发一次；不同 Key 互不连坐；窗口过后可再发
func TestNotifyKeyQuotaCoolingThrottled(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	if err := gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, NULL, 'admin', 'x', 'platform_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	if !NotifyKeyQuotaCooling(gdb, 1, 1, "ch", "sk-aaaa1111", "insufficient_quota") {
		t.Fatal("首次应发出")
	}
	if NotifyKeyQuotaCooling(gdb, 1, 1, "ch", "sk-aaaa1111", "insufficient_quota") {
		t.Fatal("同一 Key 窗口内第二次应被节流")
	}
	if !NotifyKeyQuotaCooling(gdb, 1, 2, "ch", "sk-bbbb2222", "insufficient_quota") {
		t.Fatal("同渠道另一把 Key 首次应发出（不连坐节流）")
	}
	var cnt int64
	_ = gdb.Raw("SELECT COUNT(*) FROM notifications").Scan(&cnt).Error
	if cnt != 2 {
		t.Fatalf("两把 Key 首次各一条，got %d", cnt)
	}
	// 缩短节流窗口 → 同一 Key 到期后可再次发出
	NotifyThrottle = 10 * time.Millisecond
	t.Cleanup(func() { NotifyThrottle = 30 * time.Minute })
	time.Sleep(15 * time.Millisecond)
	if !NotifyKeyQuotaCooling(gdb, 1, 1, "ch", "sk-aaaa1111", "insufficient_quota") {
		t.Fatal("节流窗口过后应可再次发出")
	}
}

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"k1":                "k1…",
		"short":             "sho…",
		"sk-abcdefghijklmn": "sk-abc…klmn",
	}
	for in, want := range cases {
		if got := MaskKey(in); got != want {
			t.Fatalf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}
