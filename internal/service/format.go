package service

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// 元口径展示工具：内部存储是整数点数（1 元 = points_per_yuan 点，默认 100 万），
// 所有面向用户的文案/账单统一折元，规则与前端 fmtYuan 完全一致。

// YuanString 点数 → 元金额串：≥1 元两位小数；不足 1 元至多 4 位（尾零剥掉，避免小额被抹成 ¥0）
func YuanString(points, ppy int64) string {
	if ppy <= 0 {
		ppy = 1_000_000
	}
	yuan := float64(points) / float64(ppy)
	sign := ""
	if yuan < 0 {
		sign = "-"
		yuan = -yuan
	}
	if yuan >= 1 {
		return fmt.Sprintf("%s¥%.2f", sign, yuan)
	}
	s := strings.TrimRight(fmt.Sprintf("%.4f", yuan), "0")
	s = strings.TrimSuffix(s, ".")
	return fmt.Sprintf("%s¥%s", sign, s)
}

// FormatYuan 便捷封装：ppy 取库内 settings（PointsPerYuan）
func FormatYuan(db *gorm.DB, points int64) string {
	return YuanString(points, PointsPerYuan(db))
}
