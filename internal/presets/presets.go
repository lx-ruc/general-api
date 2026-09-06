package presets

import (
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// ModelPreset 预置模型（单价 0，由平台管理员按厂商价目填写）
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
			{Name: "deepseek-chat", DisplayName: "DeepSeek Chat（V3）"},
			{Name: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner（R1）"},
		},
	},
	{
		Name: "智谱 GLM", Vendor: "zhipu",
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Path:    "/chat/completions",
		Models: []ModelPreset{
			{Name: "glm-4.5", DisplayName: "智谱 GLM-4.5"},
			{Name: "glm-4.5-air", DisplayName: "智谱 GLM-4.5-Air"},
			{Name: "glm-4-flash", DisplayName: "智谱 GLM-4-Flash（免费）"},
		},
	},
	{
		Name: "通义千问", Vendor: "aliyun",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Path:    "/chat/completions",
		Models: []ModelPreset{
			{Name: "qwen-max", DisplayName: "通义千问 Max"},
			{Name: "qwen-plus", DisplayName: "通义千问 Plus"},
			{Name: "qwen-turbo", DisplayName: "通义千问 Turbo"},
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
				VALUES (?, ?, ?, 0, 0, 1, '预置模型，请配置单价（点/1M token）', ?, ?)
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
