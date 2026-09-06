package service

import (
	"log/slog"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/model"
)

// BootstrapAdmin 首次启动（users 表为空）时创建平台管理员
func BootstrapAdmin(db *gorm.DB, cfg *config.Config) error {
	var cnt int64
	if err := db.Model(&model.User{}).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	hash, err := auth.HashPassword(cfg.Security.BootstrapAdminPassword)
	if err != nil {
		return err
	}
	admin := &model.User{
		Username:     cfg.Security.BootstrapAdminUsername,
		PasswordHash: hash,
		DisplayName:  "平台管理员",
		Role:         model.RolePlatformAdmin,
		Status:       1,
	}
	if err := db.Create(admin).Error; err != nil {
		return err
	}
	slog.Info("bootstrap: platform admin created",
		"username", admin.Username, "id", admin.ID)
	return nil
}
