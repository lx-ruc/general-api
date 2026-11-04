package model

// VideoTask 视频生成任务登记：创建任务 2xx 后落库，客户查询/补偿扫描到 succeeded
// 时按 usage.completion_tokens 结算。task_id 唯一，billed 是幂等抢占位
// （UPDATE … WHERE billed=0 行数判定，双网关并发只结一次）
type VideoTask struct {
	ID                int64  `gorm:"primaryKey" json:"id"`
	TaskID            string `json:"task_id"`             // 上游任务 id（Ark cgt-…）
	OrgID             int64  `json:"org_id"`
	UserID            int64  `json:"user_id"`             // 结算锚定创建者（非查询者）
	APIKeyID          int64  `json:"api_key_id"`
	CostCenterID      *int64 `json:"cost_center_id"`      // 创建时快照，结算进 usage_logs
	ChannelID         int64  `json:"channel_id"`          // 查询按此取渠道配置与密钥
	ModelName         string `json:"model_name"`          // 对外模型名
	Status            string `json:"status"`              // 最近观测的上游状态（''=未查询过）
	Billed            int    `json:"billed"`              // 1=已结算
	CompletionTokens  int64  `json:"completion_tokens"`   // 结算时 usage 快照（对账用）
	OutputPrice       int64  `json:"output_price"`        // 创建时售卖价快照（已含客户覆盖价）
	CostOutputPrice   int64  `json:"cost_output_price"`   // 创建时成本价快照
	CreateRequestID   string `json:"create_request_id"`   // 关联创建那条 cost=0 的 usage_log
	CreatedAt         int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt         int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (VideoTask) TableName() string { return "video_tasks" }
