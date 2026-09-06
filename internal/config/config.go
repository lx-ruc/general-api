package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 yaml 中 "12h" 这类字符串写法
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

type Server struct {
	Addr        string   `yaml:"addr"`
	CORSOrigins []string `yaml:"cors_origins"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Security struct {
	JWTSecret              string   `yaml:"jwt_secret"`
	JWTTTL                 Duration `yaml:"jwt_ttl"`
	AESKey                 string   `yaml:"aes_key"`
	BootstrapAdminUsername string   `yaml:"bootstrap_admin_username"`
	BootstrapAdminPassword string   `yaml:"bootstrap_admin_password"`
}

type Gateway struct {
	MaxBodyMB                int      `yaml:"max_body_mb"`
	PerKeyRPM                int      `yaml:"per_key_rpm"`
	UpstreamFirstByteTimeout Duration `yaml:"upstream_first_byte_timeout"`
}

// SMTP 邮件发送（注册验证码用）；host 为空 = 未配置，
// 验证码将写入日志并通过接口 dev_code 返回（仅限内网/开发环境）
type Smtp struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"` // 465=SSL，587/25=STARTTLS
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Config struct {
	Server      Server   `yaml:"server"`
	Database    Database `yaml:"database"`
	Security    Security `yaml:"security"`
	Gateway     Gateway  `yaml:"gateway"`
	Smtp        Smtp     `yaml:"smtp"`
	Log         Log      `yaml:"log"`
	SeedPresets bool     `yaml:"seed_presets"`
}

func defaultConfig() *Config {
	return &Config{
		Server:   Server{Addr: ":8080"},
		Database: Database{Path: "data/token_.db"},
		Security: Security{
			JWTTTL:                 Duration{12 * time.Hour},
			BootstrapAdminUsername: "admin",
			BootstrapAdminPassword: "change-me",
		},
		Gateway: Gateway{
			MaxBodyMB:                10,
			PerKeyRPM:                60,
			UpstreamFirstByteTimeout: Duration{60 * time.Second},
		},
		Log:         Log{Level: "info"},
		SeedPresets: true,
	}
}

// Load 读取配置文件（path 为空时尝试 ./config.yaml，不存在则用默认值），再用 TG_* 环境变量覆盖
func Load(path string) (*Config, error) {
	cfg := defaultConfig()
	if path == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			path = "config.yaml"
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnv(cfg)
	if cfg.Security.JWTSecret == "" {
		return nil, fmt.Errorf("security.jwt_secret is required (config.yaml 或 TG_JWT_SECRET)")
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	setStr := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	setStr("TG_SERVER_ADDR", &cfg.Server.Addr)
	setStr("TG_DATABASE_PATH", &cfg.Database.Path)
	setStr("TG_JWT_SECRET", &cfg.Security.JWTSecret)
	setStr("TG_AES_KEY", &cfg.Security.AESKey)
	setStr("TG_BOOTSTRAP_ADMIN_USERNAME", &cfg.Security.BootstrapAdminUsername)
	setStr("TG_BOOTSTRAP_ADMIN_PASSWORD", &cfg.Security.BootstrapAdminPassword)
	switch os.Getenv("TG_SEED_PRESETS") {
	case "true", "1":
		cfg.SeedPresets = true
	case "false", "0":
		cfg.SeedPresets = false
	}
}
