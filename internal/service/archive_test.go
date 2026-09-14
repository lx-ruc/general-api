package service

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"token-gateway/internal/model"
)

// 造 usage_logs：monthsAgo 月的第 day 日 + 当月各一行
func insertUsageAt(f *dbFixture2, monthsAgo, day int, id int64) {
	f.t.Helper()
	loc := BillingLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	ts := time.Date(now.Year(), now.Month(), day, 12, 0, 0, 0, loc)
	if monthsAgo > 0 {
		ts = ts.AddDate(0, -monthsAgo, 0)
	}
	if err := f.db.Exec(`INSERT INTO usage_logs (id, request_id, org_id, user_id, api_key_id, model_name,
		prompt_tokens, completion_tokens, cost, status, created_at)
		VALUES (?, 'r', 1, 1, 1, 'm', 1, 1, 1, 200, ?)`, id, ts.Unix()).Error; err != nil {
		f.t.Fatal(err)
	}
}

func countUsage(f *dbFixture2) int64 {
	f.t.Helper()
	var n int64
	_ = f.db.Raw("SELECT COUNT(*) FROM usage_logs").Scan(&n).Error
	return n
}

// retention=1：早于上月上线的整月导出+删除，当月保留；文件行数与库内一致
func TestArchiveExportsAndPrunes(t *testing.T) {
	f := newMonthlyDB(t)
	insertUsageAt(f, 3, 10, 1) // 3 个月前 → 应归档
	insertUsageAt(f, 3, 20, 2)
	insertUsageAt(f, 0, 5, 3) // 当月 → 保留

	dir := t.TempDir()
	loc := BillingLocation("Asia/Shanghai")
	n, err := ArchiveUsageOnce(f.db, 1, dir, loc)
	if err != nil || n != 2 {
		t.Fatalf("应归档 2 行，got n=%d err=%v", n, err)
	}
	if got := countUsage(f); got != 1 {
		t.Fatalf("库内应只剩当月 1 行，got %d", got)
	}
	// 归档文件内容可读、恰好 2 行、字段完整
	files, _ := filepath.Glob(filepath.Join(dir, "usage_logs-*.jsonl.gz"))
	if len(files) == 0 {
		t.Fatal("应产出归档文件")
	}
	gz, err := gzip.NewReader(mustOpen(t, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var rows []model.UsageLog
	dec := json.NewDecoder(gz)
	for dec.More() {
		var row model.UsageLog
		if err := dec.Decode(&row); err != nil {
			t.Fatalf("归档行损坏: %v", err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 2 || rows[0].ModelName != "m" || rows[0].Cost != 1 {
		t.Fatalf("归档行数/内容不符，got %d rows", len(rows))
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 幂等：跑两遍第二遍零动作；已有完整归档文件的月份直接清库不重导
func TestArchiveIdempotentRerun(t *testing.T) {
	f := newMonthlyDB(t)
	insertUsageAt(f, 2, 10, 1)

	dir := t.TempDir()
	loc := BillingLocation("Asia/Shanghai")
	if _, err := ArchiveUsageOnce(f.db, 1, dir, loc); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if len(files) != 1 {
		t.Fatalf("应恰好 1 个归档文件，got %d", len(files))
	}
	before, _ := os.Stat(files[0])

	// 手动塞回同月 1 行（模拟漏网），再跑：文件已覆盖该行 → 不重导，直接清库
	insertUsageAt(f, 2, 11, 2)
	n, err := ArchiveUsageOnce(f.db, 1, dir, loc)
	if err != nil || n != 1 {
		t.Fatalf("应按已有归档直接清理 1 行，got n=%d err=%v", n, err)
	}
	after, _ := os.Stat(files[0])
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("已有完整归档时不应重写文件")
	}
	if got := countUsage(f); got != 0 {
		t.Fatalf("库内应已清空，got %d", got)
	}
}

// 归档文件行数少于库内（截断/损坏）→ 重导全月后才删，不丢数据
func TestArchiveReexportsIncompleteFile(t *testing.T) {
	f := newMonthlyDB(t)
	insertUsageAt(f, 2, 10, 1)
	insertUsageAt(f, 2, 12, 2)

	dir := t.TempDir()
	// 预置一个只有 1 行的残缺归档
	f2 := filepath.Join(dir, monthFile(f, 2))
	writeGzipLines(t, f2, 1)

	loc := BillingLocation("Asia/Shanghai")
	n, err := ArchiveUsageOnce(f.db, 1, dir, loc)
	if err != nil || n != 2 {
		t.Fatalf("应重导后删除 2 行，got n=%d err=%v", n, err)
	}
	if got := countArchiveLines(f2); got != 2 {
		t.Fatalf("残缺文件应被重写为 2 行，got %d", got)
	}
}

// retention<=0：直接返回，不动库
func TestArchiveDisabled(t *testing.T) {
	f := newMonthlyDB(t)
	insertUsageAt(f, 6, 10, 1)
	n, err := ArchiveUsageOnce(f.db, 0, t.TempDir(), BillingLocation("Asia/Shanghai"))
	if err != nil || n != 0 {
		t.Fatalf("retention=0 应零动作，got n=%d err=%v", n, err)
	}
	if got := countUsage(f); got != 1 {
		t.Fatalf("数据不应被触碰，got %d", got)
	}
}

// monthFile 递推 monthsAgo 个月的归档文件名（与 ArchiveUsageOnce 的命名一致）
func monthFile(f *dbFixture2, monthsAgo int) string {
	f.t.Helper()
	loc := BillingLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -monthsAgo, 0)
	return "usage_logs-" + m.Format("200601") + ".jsonl.gz"
}

// writeGzipLines 写 n 行占位 JSON（模拟残缺归档）
func writeGzipLines(t *testing.T, path string, n int) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(fh)
	for i := 0; i < n; i++ {
		if _, err := gz.Write([]byte("{\"id\":999}\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
}
