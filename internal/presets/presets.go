package presets

import (
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// ModelPreset 预置模型（单价 0，由系统管理员按厂商价目填写）
type ModelPreset struct {
	Name        string
	DisplayName string
}

// ChannelPreset 预置渠道：任何 OpenAI 兼容厂商 = 一条配置
type ChannelPreset struct {
	Name    string
	Vendor  string
	BaseURL string
	Path    string
	Models  []ModelPreset
}

var Presets = []ChannelPreset{
	{
		Name: "DeepSeek", Vendor: "deepseek",
		BaseURL: "https://api.deepseek.com",
		Path:    "/v1/chat/completions",
		Models: []ModelPreset{
			{Name: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash"},
			{Name: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro"},
			// deepseek-chat 2026-07-24 起官方弃用，上游仍按别名（→v4-flash）响应，保留兼容存量调用
			{Name: "deepseek-chat", DisplayName: "DeepSeek Chat（别名 v4-flash）"},
		},
	},
	{
		Name: "智谱 GLM", Vendor: "zhipu",
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Path:    "/chat/completions",
		Models: []ModelPreset{
			{Name: "glm-5.3", DisplayName: "智谱 GLM-5.3"},
			{Name: "glm-5.3-flash", DisplayName: "智谱 GLM-5.3-Flash"},
			{Name: "glm-4-flash", DisplayName: "智谱 GLM-4-Flash（免费）"},
		},
	},
	{
		Name: "通义千问", Vendor: "aliyun",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Path:    "/chat/completions",
		Models: []ModelPreset{
			{Name: "qwen3.8-max", DisplayName: "通义千问 3.8 Max"},
			{Name: "qwen3.8-flash", DisplayName: "通义千问 3.8 Flash"},
			{Name: "qwen3.7-plus", DisplayName: "通义千问 3.7 Plus"},
			{Name: "qwen-plus", DisplayName: "通义千问 Plus（稳定别名）"},
		},
	},
	{
		Name: "Kimi", Vendor: "moonshot",
		BaseURL: "https://api.moonshot.cn",
		Path:    "/v1/chat/completions",
		Models: []ModelPreset{
			{Name: "kimi-k3", DisplayName: "Kimi K3"},
			{Name: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code"},
			{Name: "kimi-k2.6", DisplayName: "Kimi K2.6"},
		},
	},
}

// Seed 启动时确保预置渠道/模型存在（幂等；已有同名记录不覆盖，管理员改动得以保留）。
// 渠道初始 status=0（需填入上游密钥后启用）。
func Seed(db *gorm.DB) error {
	now := time.Now().Unix()
	for _, p := range Presets {
		res := db.Exec(`INSERT INTO channels (name, vendor, base_url, path, upstream_key_enc, weight, priority, status, remark, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', 1, 0, 0, '预置渠道，请填入上游密钥后启用', ?, ?)
			ON CONFLICT(name) DO NOTHING`, p.Name, p.Vendor, p.BaseURL, p.Path, now, now)
		if res.Error != nil {
			return res.Error
		}
		var channelID int64
		if err := db.Raw("SELECT id FROM channels WHERE name = ?", p.Name).Scan(&channelID).Error; err != nil || channelID == 0 {
			continue
		}
		for _, m := range p.Models {
			if err := db.Exec(`INSERT INTO models (name, display_name, vendor, input_price, output_price, status, remark, created_at, updated_at)
				VALUES (?, ?, ?, 0, 0, 1, '预置模型，请配置单价（元/M token）', ?, ?)
				ON CONFLICT(name) DO NOTHING`, m.Name, m.DisplayName, p.Vendor, now, now).Error; err != nil {
				return err
			}
			if err := db.Exec(`INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, ?)
				ON CONFLICT(channel_id, model_name) DO NOTHING`, channelID, m.Name).Error; err != nil {
				return err
			}
		}
	}
	slog.Info("presets seeded", "channels", len(Presets))
	return nil
}
