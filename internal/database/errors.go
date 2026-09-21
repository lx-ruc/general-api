package database

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// IsDuplicateKey 判断错误是否为唯一约束冲突——check-then-insert 模式下并发同键写入的
// 后来者会撞唯一索引，预检查（SELECT COUNT）拦不住。SQLite（UNIQUE constraint failed:
// users.username）与 PostgreSQL（duplicate key value violates unique constraint
// "users_username_key"）两种驱动文案都覆盖；gorm 开 TranslateError 时返回
// gorm.ErrDuplicatedKey，一并兼容。调用方应把它映射回与预检查一致的友好文案，
// 而非把驱动原始错误泄漏给客户端。
func IsDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "duplicate key value violates unique constraint")
}
