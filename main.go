package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"token-gateway/internal/api"
	"token-gateway/internal/config"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/presets"
	"token-gateway/internal/service"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	resetPwd := flag.String("reset-password", "", "重置用户密码（忘记密码的运维兜底），格式: 用户名:新密码，如 admin:NewPass123")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	setupLogger(cfg.Log.Level)
	slog.Info("token 中转站启动中", "addr", cfg.Server.Addr, "db", cfg.Database.Path)

	db, err := database.Open(cfg.Database.Path)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	if err := database.Migrate(db); err != nil {
		slog.Error("数据库迁移失败", "err", err)
		os.Exit(1)
	}

	// 运维兜底：重置任意用户密码（平台管理员密码丢失时的唯一恢复路径），完成后退出
	if *resetPwd != "" {
		parts := strings.SplitN(*resetPwd, ":", 2)
		if len(parts) != 2 || len(parts[1]) < 6 {
			slog.Error("格式错误，应为: -reset-password 用户名:新密码（新密码至少 6 位）")
			os.Exit(1)
		}
		if err := service.ResetUserPassword(db, parts[0], parts[1]); err != nil {
			slog.Error("重置失败", "username", parts[0], "err", err)
			os.Exit(1)
		}
		slog.Info("密码已重置", "username", parts[0])
		return
	}
	if cfg.SeedPresets {
		if err := presets.Seed(db); err != nil {
			slog.Error("预置渠道写入失败", "err", err)
			os.Exit(1)
		}
	}
	if err := service.BootstrapAdmin(db, cfg); err != nil {
		slog.Error("初始化管理员失败", "err", err)
		os.Exit(1)
	}
	cipher, err := crypto.NewCipher(cfg.Security.AESKey)
	if err != nil {
		slog.Error("初始化加密器失败", "err", err)
		os.Exit(1)
	}

	router := api.SetupRouter(cfg, db, cipher, webDistFS)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 故意不设 WriteTimeout：会杀 SSE 流式长连接
	}
	go func() {
		slog.Info("token-gateway listening", "addr", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	slog.Info("收到退出信号，优雅关闭中…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("优雅关闭超时", "err", err)
	}
	if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
		_ = sqlDB.Close()
	}
	slog.Info("已退出")
}

func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})))
}
