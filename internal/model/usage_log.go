package model

// UsageLog 每次调用记录（含结算时单价快照，改价不影响历史账单）
type UsageLog struct {
	ID               int64  `gorm:"primaryKey" json:"id"`
	RequestID        string `json:"request_id"`
	OrgID            int64  `json:"org_id"`
	UserID           int64  `json:"user_id"`
	APIKeyID         int64  `json:"api_key_id"`
	ChannelID        *int64 `json:"channel_id"`
	CostCenterID     *int64 `json:"cost_center_id"` // 结算时快照；key 改派不动历史
	ModelName        string `json:"model_name"`
	IsStream         int    `json:"is_stream"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	InputPrice       int64  `json:"input_price"`  // 结算时快照（售卖价）
	OutputPrice      int64  `json:"output_price"` // 结算时快照
	CostInputPrice   int64  `json:"cost_input_price"`
	CostOutputPrice  int64  `json:"cost_output_price"`
	VendorCost       int64  `json:"vendor_cost"` // 厂商成本；毛利 = Cost - VendorCost
	Cost             int64  `json:"cost"`        // 客户扣减（= 平台营收）
	NoUsage          int    `json:"no_usage"`     // 1=上游未回 usage，本次未计费
	CacheHit         int    `json:"cache_hit"`    // 1=精确缓存命中（未打上游，cost=0）
	Status           int    `json:"status"`
	Error            string `json:"error"`
	LatencyMs        int64  `json:"latency_ms"`
	ClientIP         string `json:"client_ip"`
	CreatedAt        int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (UsageLog) TableName() string { return "usage_logs" }
