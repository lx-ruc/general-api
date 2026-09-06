package database

import (
	_ "embed"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

//go:embed schema.sql
var schemaSQL string

// Migrate 幂等执行建表语句（按 ; 切分，剔除注释行后逐条执行）
func Migrate(db *gorm.DB) error {
	for _, stmt := range splitStatements(schemaSQL) {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("migrate %q: %w", truncate(stmt, 60), err)
		}
	}
	return nil
}

func splitStatements(sql string) []string {
	var stmts []string
	for _, part := range strings.Split(sql, ";") {
		var lines []string
		for _, line := range strings.Split(part, "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "--") {
				continue
			}
			lines = append(lines, line)
		}
		if st := strings.TrimSpace(strings.Join(lines, "\n")); st != "" {
			stmts = append(stmts, st)
		}
	}
	return stmts
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
