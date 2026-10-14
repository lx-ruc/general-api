package database

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
)

// users.email 唯一部分索引（WHERE email != ''）+ 存量去重：闭合「两人并发绑同一邮箱」
// 在 PG READ COMMITTED 下的 check-then-act 竞态。此文件验证迁移的三件事：
// 新库建索引、存量重复邮箱去重（保留最早注册者，与找回流程同口径）、重复迁移幂等。

// indexSQL 取索引定义原文（sqlite_master），空串 = 索引不存在
func indexSQL(t *testing.T, db *gorm.DB, name string) string {
	t.Helper()
	var sql string
	_ = db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&sql).Error
	return sql
}

func insertUserWithEmail(t *testing.T, db *gorm.DB, id int64, username, email string) {
	t.Helper()
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO users (id, username, password_hash, display_name, email, role, status, created_at, updated_at)
		VALUES (?, ?, 'x', ?, ?, 'org_admin', 1, ?, ?)`, id, username, username, email, now, now).Error; err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
}

// 新库：迁移后存在唯一部分索引；重复迁移幂等不报错
func TestMigrateCreatesUniqueEmailIndex(t *testing.T) {
	db, err := Open(config.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sql := indexSQL(t, db, "idx_users_email")
	if sql == "" {
		t.Fatal("迁移后应存在 idx_users_email 索引")
	}
	if !strings.Contains(sql, "UNIQUE") || !strings.Contains(sql, "email != ''") {
		t.Fatalf("应为唯一部分索引，得 %q", sql)
	}

	// 索引生效：同邮箱第二个账号插入被拒（空串哨兵不受限）
	insertUserWithEmail(t, db, 1, "u1", "a@x.com")
	err = db.Exec(`INSERT INTO users (username, password_hash, display_name, role, status, created_at, updated_at)
		VALUES ('u2', 'x', 'u2', 'org_admin', 1, ?, ?)`, time.Now().Unix(), time.Now().Unix()).Error
	if err != nil {
		t.Fatalf("空邮箱（未绑定）不应受唯一索引限制: %v", err)
	}
	if err := insertDupEmail(db, "a@x.com"); err == nil {
		t.Fatal("重复邮箱插入应被唯一索引拒绝")
	}

	// 幂等：重复迁移不报错、索引仍在
	if err := Migrate(db); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	if indexSQL(t, db, "idx_users_email") == "" {
		t.Fatal("重复迁移后索引应仍存在")
	}
}

func insertDupEmail(db *gorm.DB, email string) error {
	now := time.Now().Unix()
	return db.Exec(`INSERT INTO users (username, password_hash, display_name, email, role, status, created_at, updated_at)
		VALUES ('dup', 'x', 'dup', ?, 'org_admin', 1, ?, ?)`, email, now, now).Error
}

// 存量库：已有重复邮箱时迁移先去重（保留 MIN(id)）再建索引，不炸启动
func TestMigrateDedupesLegacyEmails(t *testing.T) {
	db, err := Open(config.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 伪造存量脏数据：摘掉索引 → 插两行同邮箱 + 一行干净数据（模拟索引上线前的旧库）
	if err := db.Exec("DROP INDEX idx_users_email").Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}
	insertUserWithEmail(t, db, 100, "early", "dup@x.com")
	insertUserWithEmail(t, db, 101, "later", "dup@x.com")
	insertUserWithEmail(t, db, 102, "clean", "c@x.com")

	if err := Migrate(db); err != nil {
		t.Fatalf("存量重复邮箱下迁移应去重成功而非报错: %v", err)
	}
	var early, later string
	_ = db.Raw("SELECT email FROM users WHERE id = 100").Scan(&early).Error
	_ = db.Raw("SELECT email FROM users WHERE id = 101").Scan(&later).Error
	if early != "dup@x.com" {
		t.Fatalf("最早注册者应保留邮箱，得 %q", early)
	}
	if later != "" {
		t.Fatalf("后来者邮箱应被清空，得 %q", later)
	}
	if indexSQL(t, db, "idx_users_email") == "" {
		t.Fatal("去重后应建出唯一索引")
	}
}
