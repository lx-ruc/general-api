package config

import (
	"fmt"
	"os"
	"strconv"
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
	SiteURL     string   `yaml:"site_url"` // 管理台外部地址；告警邮件直达链接用，空=不带链接
}

// Database 数据库：driver=sqlite（单文件，默认）或 postgres（多实例高可用）
// postgres 模式下用 dsn 连接；sqlite 模式用 path
type Database struct {
	Driver string `yaml:"driver"` // sqlite | postgres
	Path   string `yaml:"path"`   // sqlite 文件路径
	DSN    string `yaml:"dsn"`    // postgres 连接串
}

type Security struct {
	JWTSecret              string   `yaml:"jwt_secret"`
	JWTTTL                 Duration `yaml:"jwt_ttl"`
	MetricsToken           string   `yaml:"metrics_token"`
	AESKey                 string   `yaml:"aes_key"`
	BootstrapAdminUsername string   `yaml:"bootstrap_admin_username"`
	BootstrapAdminPassword string   `yaml:"bootstrap_admin_password"`
}

type Gateway struct {
	MaxBodyMB                int      `yaml:"max_body_mb"`
	PerKeyRPM                int      `yaml:"per_key_rpm"`
	UpstreamFirstByteTimeout Duration `yaml:"upstream_first_byte_timeout"`
	ChannelBreakerThreshold  int      `yaml:"channel_breaker_threshold"` // 渠道连续失败自动禁用阈值；0=关闭
	// 高并发三件套（见 internal/coord）：并发闸门 + Key 冷却 + 精确缓存
	ChannelMaxConcurrency int      `yaml:"channel_max_concurrency"` // 每渠道最大并发上游请求数；0=不限
	QueueWaitTimeout      Duration `yaml:"queue_wait_timeout"`      // 闸门排队等待上限，超时换下一候选
	KeyCooldown           Duration `yaml:"key_cooldown"`            // 上游 429 后该 Key 的冷却时长
	CacheTTL              Duration `yaml:"cache_ttl"`               // 精确缓存 TTL；0=关闭
	CacheMaxItems         int      `yaml:"cache_max_items"`         // 内存 LRU 条数上限（redis 模式仅约束写入侧频率）
	CacheIsolateOrg       bool     `yaml:"cache_isolate_org"`       // true=缓存按客户隔离（默认全局共享）
}

// Redis 协调器（key 冷却/并发闸门/缓存 全局共享）；addr 为空 = 全部回退进程内存（单机/无依赖部署）
type Redis struct {
	Addr     string `yaml:"addr"` // host:port；ENV: TG_REDIS_ADDR
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
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

// Billing 账单口径：月边界时区（IANA 名，如 Asia/Shanghai）
type Billing struct {
	Timezone string `yaml:"timezone"` // ENV: TG_BILLING_TIMEZONE
}

type Config struct {
	Server      Server   `yaml:"server"`
	Database    Database `yaml:"database"`
	Security    Security `yaml:"security"`
	Gateway     Gateway  `yaml:"gateway"`
	Redis       Redis    `yaml:"redis"`
	Smtp        Smtp     `yaml:"smtp"`
	Log         Log      `yaml:"log"`
	Billing     Billing  `yaml:"billing"`
	SeedPresets bool     `yaml:"seed_presets"`
}

func defaultConfig() *Config {
	return &Config{
		Server:   Server{Addr: ":8080"},
		Database: Database{Driver: "sqlite", Path: "data/token_.db"},
		Security: Security{
			JWTTTL:                 Duration{12 * time.Hour},
			BootstrapAdminUsername: "admin",
			BootstrapAdminPassword: "change-me",
		},
		Gateway: Gateway{
			MaxBodyMB:                10,
			PerKeyRPM:                60,
			UpstreamFirstByteTimeout: Duration{60 * time.Second},
			ChannelBreakerThreshold:  5,
			QueueWaitTimeout:         Duration{10 * time.Second},
			KeyCooldown:              Duration{60 * time.Second},
			CacheMaxItems:            1000,
		},
		Log:         Log{Level: "info"},
		Billing:     Billing{Timezone: "Asia/Shanghai"},
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
	setInt := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	setDuration := func(key string, dst *Duration) {
		if v := os.Getenv(key); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = Duration{d}
			}
		}
	}
	setBool := func(key string, dst *bool) {
		switch os.Getenv(key) {
		case "true", "1":
			*dst = true
		case "false", "0":
			*dst = false
		}
	}
	setStr("TG_SERVER_ADDR", &cfg.Server.Addr)
	setStr("TG_SITE_URL", &cfg.Server.SiteURL)
	setStr("TG_DATABASE_DRIVER", &cfg.Database.Driver)
	setStr("TG_DATABASE_PATH", &cfg.Database.Path)
	setStr("TG_DATABASE_DSN", &cfg.Database.DSN)
	setStr("TG_JWT_SECRET", &cfg.Security.JWTSecret)
	setStr("TG_METRICS_TOKEN", &cfg.Security.MetricsToken)
	setStr("TG_AES_KEY", &cfg.Security.AESKey)
	setStr("TG_BOOTSTRAP_ADMIN_USERNAME", &cfg.Security.BootstrapAdminUsername)
	setStr("TG_BOOTSTRAP_ADMIN_PASSWORD", &cfg.Security.BootstrapAdminPassword)
	setInt("TG_CHANNEL_MAX_CONCURRENCY", &cfg.Gateway.ChannelMaxConcurrency)
	setDuration("TG_QUEUE_WAIT_TIMEOUT", &cfg.Gateway.QueueWaitTimeout)
	setDuration("TG_KEY_COOLDOWN", &cfg.Gateway.KeyCooldown)
	setDuration("TG_CACHE_TTL", &cfg.Gateway.CacheTTL)
	setInt("TG_CACHE_MAX_ITEMS", &cfg.Gateway.CacheMaxItems)
	setBool("TG_CACHE_ISOLATE_ORG", &cfg.Gateway.CacheIsolateOrg)
	setStr("TG_REDIS_ADDR", &cfg.Redis.Addr)
	setStr("TG_BILLING_TIMEZONE", &cfg.Billing.Timezone)
	setStr("TG_REDIS_PASSWORD", &cfg.Redis.Password)
	setInt("TG_REDIS_DB", &cfg.Redis.DB)
	switch os.Getenv("TG_SEED_PRESETS") {
	case "true", "1":
		cfg.SeedPresets = true
	case "false", "0":
		cfg.SeedPresets = false
	}
}
