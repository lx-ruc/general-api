package service

import (
	"testing"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

// 自举管理员即超级管理员（子管理员由其在管理台分配）
func TestBootstrapAdminIsSuper(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	cfg := &config.Config{}
	cfg.Security.BootstrapAdminUsername = "root"
	cfg.Security.BootstrapAdminPassword = "strong-enough"
	if err := BootstrapAdmin(gdb, cfg); err != nil {
		t.Fatalf("自举失败: %v", err)
	}
	var u model.User
	if err := gdb.Where("username = ?", "root").First(&u).Error; err != nil {
		t.Fatalf("查自举账号失败: %v", err)
	}
	if u.Role != model.RolePlatformAdmin || u.IsSuper != 1 {
		t.Fatalf("自举管理员应为超管 platform_admin/is_super=1，got role=%s is_super=%d", u.Role, u.IsSuper)
	}
	// 已有用户时自举跳过（幂等，不重复建号也不改既有标志）
	if err := BootstrapAdmin(gdb, cfg); err != nil {
		t.Fatalf("二次自举应跳过: %v", err)
	}
	var cnt int64
	_ = gdb.Model(&model.User{}).Count(&cnt).Error
	if cnt != 1 {
		t.Fatalf("自举应只建一个账号，got %d", cnt)
	}
}
