package service

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/model"
)

const archiveBatchSize = 500

// RunUsageArchiver 常驻协程：启动即跑一轮（漏跑自愈），此后每日 03:37（账期时区）归档。
// retentionMonths<=0 直接返回（默认关闭）。幂等：逐月「导出校验后再删」，
// 已有完整归档文件的月份直接清理库内行，中断/失败月份不动库。
func RunUsageArchiver(db *gorm.DB, retentionMonths int, archiveDir, tz string) {
	if retentionMonths <= 0 {
		return
	}
	if archiveDir == "" {
		archiveDir = "data/usage_archives"
	}
	loc := BillingLocation(tz)
	run := func() {
		rows, err := ArchiveUsageOnce(db, retentionMonths, archiveDir, loc)
		if err != nil {
			slog.Error("usage_logs 归档失败", "err", err)
			return
		}
		if rows > 0 {
			slog.Info("usage_logs 归档完成", "archived_rows", rows, "dir", archiveDir)
		}
	}
	run() // 启动自愈
	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 37, 0, 0, loc).AddDate(0, 0, 1)
		time.Sleep(time.Until(next))
		run()
	}
}

// ArchiveUsageOnce 归档一轮：把早于「当月前推 retention 个自然月」的整月 usage_logs
// 导出为 {dir}/usage_logs-YYYYMM.jsonl.gz 并从库内删除；返回本轮归档（删除）的行数。
// 安全顺序：导出到临时文件 → 行数与库内一致 → 原子 rename → 按月删库（500 行/批）。
func ArchiveUsageOnce(db *gorm.DB, retentionMonths int, archiveDir string, loc *time.Location) (int64, error) {
	if retentionMonths <= 0 {
		return 0, nil
	}
	var minTS int64
	if err := db.Raw("SELECT MIN(created_at) FROM usage_logs").Scan(&minTS).Error; err != nil {
		return 0, fmt.Errorf("查最早 usage_log: %w", err)
	}
	if minTS == 0 {
		return 0, nil // 无历史数据
	}
	// 归档线 = 当月前推 retention 个月的首日；早于该日的自然月全部归档
	now := time.Now().In(loc)
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -retentionMonths, 0)

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return 0, fmt.Errorf("建归档目录: %w", err)
	}

	var total int64
	// 从最早行所在月迭代到归档线（上限 1200 个月，防脏数据死循环）
	m := time.Unix(minTS, 0).In(loc)
	m = time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, loc)
	for i := 0; i < 1200 && m.Before(cutoff); i++ {
		start := m.Unix()
		end := m.AddDate(0, 1, 0).Unix()
		n, err := archiveMonth(db, archiveDir, m.Format("200601"), start, end)
		if err != nil {
			return total, err
		}
		total += n
		m = m.AddDate(0, 1, 0)
	}
	return total, nil
}

// archiveMonth 归档单个自然月：返回删除的行数（0=该月无需处理）
func archiveMonth(db *gorm.DB, archiveDir, ym string, start, end int64) (int64, error) {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM usage_logs WHERE created_at >= ? AND created_at < ?", start, end).
		Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("统计 %s: %w", ym, err)
	}
	if count == 0 {
		return 0, nil
	}
	path := filepath.Join(archiveDir, "usage_logs-"+ym+".jsonl.gz")

	// 已有归档且行数覆盖库内 → 免导出直接清理（幂等重跑路径）
	if n := countArchiveLines(path); n >= count {
		return deleteUsageRange(db, start, end)
	}

	// 导出到临时文件：写一行校验一行，最终行数必须等于库内计数
	tmp := path + ".tmp"
	if err := exportMonth(db, tmp, start, end); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if n := countArchiveLines(tmp); n != count {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("归档 %s 行数不符：库内 %d / 文件 %d，跳过删除", ym, count, n)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("归档 %s 落位失败: %w", ym, err)
	}
	slog.Info("usage_logs 已导出", "month", ym, "rows", count, "file", path)
	return deleteUsageRange(db, start, end)
}

// exportMonth 流式导出一个月（id 升序、500 行/批），gzip JSONL
func exportMonth(db *gorm.DB, path string, start, end int64) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("建临时归档文件: %w", err)
	}
	gz := gzip.NewWriter(f)
	w := bufio.NewWriter(gz)
	enc := json.NewEncoder(w)

	var lastID int64
	for {
		var rows []model.UsageLog
		if err := db.Where("created_at >= ? AND created_at < ? AND id > ?", start, end, lastID).
			Order("id").Limit(archiveBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("读取归档行: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			if err := enc.Encode(&rows[i]); err != nil {
				return fmt.Errorf("编码归档行: %w", err)
			}
			lastID = rows[i].ID
		}
		if len(rows) < archiveBatchSize {
			break
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

// countArchiveLines 数归档文件行数；文件不存在/损坏返回 0
func countArchiveLines(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0
	}
	defer gz.Close()
	var n int64
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0
	}
	return n
}

// deleteUsageRange 按 id 精确分批删除（500 行/批，SQLite 单写者不长时间持锁）
func deleteUsageRange(db *gorm.DB, start, end int64) (int64, error) {
	var deleted int64
	var lastID int64
	for {
		var ids []int64
		if err := db.Raw("SELECT id FROM usage_logs WHERE created_at >= ? AND created_at < ? AND id > ? ORDER BY id LIMIT ?",
			start, end, lastID, archiveBatchSize).Scan(&ids).Error; err != nil {
			return deleted, fmt.Errorf("查删除批次: %w", err)
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		if err := db.Exec("DELETE FROM usage_logs WHERE id IN ?", ids).Error; err != nil {
			return deleted, fmt.Errorf("删除归档行: %w", err)
		}
		deleted += int64(len(ids))
		lastID = ids[len(ids)-1]
	}
}
