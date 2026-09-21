package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/model"
)

// ResetUserPassword 运维兜底：按用户名重置任意账号密码（CLI -reset-password 用）
func ResetUserPassword(db *gorm.DB, username, newPassword string) error {
	var cnt int64
	if err := db.Model(&model.User{}).Where("username = ?", username).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return db.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ?",
		hash, time.Now().Unix(), username).Error
}

// BootstrapAdmin 首次启动（users 表为空）时创建系统管理员
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
		DisplayName:  "系统管理员",
		Role:         model.RolePlatformAdmin,
		Status:       1,
	}
	if err := db.Create(admin).Error; err != nil {
		return err
	}
	slog.Info("bootstrap: platform admin created",
		"username", admin.Username, "id", admin.ID)
	// 默认口令弱口令告警：建号后仍未改密码是最常见的失守入口
	if cfg.Security.BootstrapAdminPassword == "change-me" || cfg.Security.BootstrapAdminPassword == "admin123456" {
		slog.Warn("bootstrap: 管理员仍在使用默认密码，请立即登录修改（config.yaml security.bootstrap_admin_password）",
			"username", admin.Username)
	}
	return nil
}
