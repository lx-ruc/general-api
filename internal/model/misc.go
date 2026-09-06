package model

// QuotaRequest 员工额度申请（公司管理员审批）
type QuotaRequest struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	OrgID      int64  `json:"org_id"`
	UserID     int64  `json:"user_id"`
	Amount     int64  `json:"amount"`
	Reason     string `json:"reason"`
	Status     string `json:"status"` // pending/approved/rejected
	HandledBy  *int64 `json:"handled_by"`
	HandledAt  *int64 `json:"handled_at"`
	Reply      string `json:"reply"`
	CreatedAt  int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (QuotaRequest) TableName() string { return "quota_requests" }

// Setting 全局 KV 设置（如 points_per_yuan）
type Setting struct {
	Key   string `gorm:"primaryKey" json:"key"`
	Value string `json:"value"`
}

func (Setting) TableName() string { return "settings" }
