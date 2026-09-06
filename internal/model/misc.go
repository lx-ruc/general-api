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

// RechargeRequest 公司充值申请（对公转账 + 平台人工确认到账）
type RechargeRequest struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	OrgID     int64  `json:"org_id"`
	Amount    int64  `json:"amount"`
	Voucher   string `json:"voucher"` // 转账凭证说明（流水号/截图说明）
	Status    string `json:"status"`  // pending/approved/rejected
	HandledBy *int64 `json:"handled_by"`
	HandledAt *int64 `json:"handled_at"`
	Reply     string `json:"reply"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (RechargeRequest) TableName() string { return "recharge_requests" }

// AuditLog 管理台操作审计
type AuditLog struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	ActorID   *int64 `json:"actor_id"`
	Actor     string `json:"actor"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }
