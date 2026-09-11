package model

// Channel 上游渠道：任何 OpenAI 兼容厂商 = 一行配置
type Channel struct {
	ID             int64  `gorm:"primaryKey" json:"id"`
	Name           string `json:"name"`
	Vendor         string `json:"vendor"`
	BaseURL        string `json:"base_url"`
	Path           string `json:"path"`
	UpstreamKeyEnc string `json:"-"` // AES-GCM b64 密文（未启用加密则明文）
	Weight         int    `json:"weight"`
	Priority       int    `json:"priority"` // 大者优先，同优先级按 weight 加权
	Status         int    `json:"status"`
	AutoDisabledAt int64  `json:"auto_disabled_at"` // >0=系统熔断禁用时间戳（自动探测恢复）；0=未禁用或人工禁用
	LastTestAt     *int64 `json:"last_test_at"`
	LastTestOk     int    `json:"last_test_ok"`
	Remark         string `json:"remark"`
	CreatedAt      int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt      int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Channel) TableName() string { return "channels" }

// ChannelKey 渠道 Key 池：一把渠道可挂多把上游 Key（加权调度、429 冷却、401 自动禁用）。
// 渠道无池内 Key 时回退 channels.upstream_key_enc（单 Key 兼容模式）。
type ChannelKey struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	ChannelID int64  `json:"channel_id"`
	KeyEnc    string `json:"-"` // AES-GCM b64 密文（未启用加密则明文）
	Weight    int    `json:"weight"`
	Status    int    `json:"status"`
	Remark    string `json:"remark"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ChannelKey) TableName() string { return "channel_keys" }

// ChannelAbility 渠道能力（渠道 × 模型；路由依据）
type ChannelAbility struct {
	ID                int64  `gorm:"primaryKey" json:"id"`
	ChannelID         int64  `json:"channel_id"`
	ModelName         string `json:"model_name"`
	UpstreamModelName *string `json:"upstream_model_name"` // NULL=同名透传
}

func (ChannelAbility) TableName() string { return "channel_abilities" }
