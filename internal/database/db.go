package database

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"token-gateway/internal/config"
)

// Dialect 当前数据库方言（"sqlite" | "postgres"），供 SQL 方言分支处使用
var Dialect = "sqlite"

// Open 打开数据库。
//
// sqlite（默认）：pure-Go 驱动无 CGO；_txlock=immediate 让所有写事务直接取
// RESERVED 锁，配合单连接串行化彻底避免写写 SQLITE_BUSY 升级死锁；WAL 提升读写并行。
//
// postgres：多实例高可用模式；行锁天然支持多写者，连接池放开。
func Open(cfg config.Database) (*gorm.DB, error) {
	switch cfg.Driver {
	case "", "sqlite":
		Dialect = "sqlite"
		return openSQLite(cfg.Path)
	case "postgres":
		Dialect = "postgres"
		return openPostgres(cfg.DSN)
	default:
		return nil, fmt.Errorf("未知 database.driver: %s（支持 sqlite | postgres）", cfg.Driver)
	}
}

func openSQLite(path string) (*gorm.DB, error) {
	if path == "" {
		path = "data/token_.db"
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := fmt.Sprintf(
		"file:%s?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)",
		path,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// 单连接串行化：单机部署下最简单且绝对正确
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxIdleTime(0)
	sqlDB.SetConnMaxLifetime(0)
	return db, nil
}

func openPostgres(dsn string) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres 模式需要配置 database.dsn（或 TG_DATABASE_DSN）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(0)
	return db, nil
}
