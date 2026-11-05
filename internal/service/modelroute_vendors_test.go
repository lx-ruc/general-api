package service

import (
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 厂商标签聚合：多渠道多分组去重、空 vendor 渠道不计、无承接渠道返回空
func TestChannelVendorsByModel(t *testing.T) {
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/t.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	// 三个渠道：volces/Thu 与 volces/berry 都承接 m1，volces 也承接 m1（标签应去重为
	// 三个升序）；ch3 vendor 空承接 m2（不计）；m3 无承接渠道
	for _, ch := range []struct {
		id     int64
		vendor string
	}{
		{1, "volces/Thu"}, {2, "volces/berry"}, {3, ""}, {4, "volces"},
	} {
		if err := db.Exec(`INSERT INTO channels (id, name, vendor, base_url, path, status, created_at, updated_at)
			VALUES (?, ?, ?, 'https://x.example', '/chat/completions', 1, ?, ?)`,
			ch.id, "ch"+string(rune('0'+ch.id)), ch.vendor, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, ab := range []struct {
		ch    int64
		model string
	}{{1, "m1"}, {2, "m1"}, {4, "m1"}, {3, "m2"}} {
		if err := db.Exec(`INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, ?)`,
			ab.ch, ab.model).Error; err != nil {
			t.Fatal(err)
		}
	}

	got := ChannelVendorsByModel(db, []string{"m1", "m2", "m3"})
	if len(got["m1"]) != 3 || got["m1"][0] != "volces" || got["m1"][1] != "volces/Thu" || got["m1"][2] != "volces/berry" {
		t.Fatalf("m1 标签应为 volces/volces/Thu/volces/berry 去重升序，got %v", got["m1"])
	}
	if len(got["m2"]) != 0 {
		t.Fatalf("空 vendor 渠道不计标签，got %v", got["m2"])
	}
	if len(got["m3"]) != 0 {
		t.Fatalf("无承接渠道应返回空，got %v", got["m3"])
	}
	if got2 := ChannelVendorsByModel(db, nil); len(got2) != 0 {
		t.Fatal("空名单应直接返回空 map")
	}
}
