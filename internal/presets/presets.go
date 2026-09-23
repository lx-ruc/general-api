package presets

import (
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// ChannelPreset 预置渠道模板：任何 OpenAI 兼容厂商 = 一条配置。
// 只预置连接信息（base_url/路径），不预置模型——厂商上下架频繁，模型一律
// 「从上游获取模型」实时拉取后勾选并在保存时定价，避免预置一堆已下架的死名字
type ChannelPreset struct {
	Name    string
	Vendor  string
	BaseURL string
	Path    string
}

var Presets = []ChannelPreset{
	{Name: "DeepSeek", Vendor: "deepseek", BaseURL: "https://api.deepseek.com", Path: "/v1/chat/completions"},
	{Name: "智谱 GLM", Vendor: "zhipu", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Path: "/chat/completions"},
	{Name: "通义千问", Vendor: "aliyun", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Path: "/chat/completions"},
	{Name: "Kimi", Vendor: "moonshot", BaseURL: "https://api.moonshot.cn", Path: "/v1/chat/completions"},
}

// Seed 启动时确保预置渠道存在（幂等；已有同名记录不覆盖，管理员改动得以保留）。
// 渠道初始 status=0（需填入上游密钥后启用），不含任何模型能力。
func Seed(db *gorm.DB) error {
	now := time.Now().Unix()
	for _, p := range Presets {
		res := db.Exec(`INSERT INTO channels (name, vendor, base_url, path, upstream_key_enc, weight, priority, status, remark, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', 1, 0, 0, '预置渠道，请填入上游密钥后启用', ?, ?)
			ON CONFLICT(name) DO NOTHING`, p.Name, p.Vendor, p.BaseURL, p.Path, now, now)
		if res.Error != nil {
			return res.Error
		}
	}
	slog.Info("presets seeded", "channels", len(Presets))
	return nil
}
