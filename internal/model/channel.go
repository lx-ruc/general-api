package model

// Channel 上游渠道（厂商下的端点）：任何 OpenAI 兼容厂商 = 一行配置。
// Key 在厂商池（vendor_keys），渠道只承载接入地址与模型映射
type Channel struct {
	ID              int64  `gorm:"primaryKey" json:"id"`
	Name            string `json:"name"`
	VendorID        int64  `json:"vendor_id"` // 所属厂商（0=未挂，不可路由）
	BaseURL         string `json:"base_url"`
	Path            string `json:"path"`
	Weight          int    `json:"weight"`
	Priority        int    `json:"priority"` // 大者优先，同优先级按 weight 加权
	Status          int    `json:"status"`
	AutoDisabledAt  int64  `json:"auto_disabled_at"`                                  // >0=系统熔断禁用时间戳（自动探测恢复）；0=未禁用或人工禁用
	NoKeyDisabledAt int64  `gorm:"column:nokey_disabled_at" json:"nokey_disabled_at"` // >0=因无可用密钥自动停用时间戳（补 Key 自动恢复）；0=未停用/其他原因停用
	LastTestAt      *int64 `json:"last_test_at"`
	LastTestOk      int    `json:"last_test_ok"`
	Remark          string `json:"remark"`
	CreatedAt       int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt       int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Channel) TableName() string { return "channels" }

// ChannelAbility 渠道能力（渠道 × 模型；路由依据）

// ChannelAbility 渠道能力（渠道 × 模型；路由依据）
type ChannelAbility struct {
	ID                int64   `gorm:"primaryKey" json:"id"`
	ChannelID         int64   `json:"channel_id"`
	ModelName         string  `json:"model_name"`
	UpstreamModelName *string `json:"upstream_model_name"` // NULL=同名透传
}

func (ChannelAbility) TableName() string { return "channel_abilities" }
