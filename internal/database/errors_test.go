package database

import (
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// 两种驱动的唯一约束文案 + gorm 翻译错误都要命中；其他约束/普通错误不得误判
func TestIsDuplicateKey(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"SQLite 文案", errors.New("constraint failed: UNIQUE constraint failed: users.username (2067)"), true},
		{"PG 文案", errors.New(`duplicate key value violates unique constraint "users_username_key" (SQLSTATE 23505)`), true},
		{"gorm 翻译错误", gorm.ErrDuplicatedKey, true},
		{"gorm 翻译错误（包裹）", fmt.Errorf("建号: %w", gorm.ErrDuplicatedKey), true},
		{"外键约束不误判", errors.New("FOREIGN KEY constraint failed"), false},
		{"非空约束不误判", errors.New("NOT NULL constraint failed: users.email"), false},
		{"连接错误", errors.New("connection refused"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDuplicateKey(c.err); got != c.want {
				t.Errorf("IsDuplicateKey(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
