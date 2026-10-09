package model

// UserModelGrant 子账号模型白名单（客户管理员授权；无记录=无权限）
type UserModelGrant struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	UserID    int64  `json:"user_id"`
	ModelName string `json:"model_name"`
	GrantedBy *int64 `json:"granted_by"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (UserModelGrant) TableName() string { return "user_model_grants" }

// OrgModelGrant 客户级模型白名单（平台开通；无记录=未开通，回收=删行）。
// source: grant=审批/手动开通，backfill=存量继承
type OrgModelGrant struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	OrgID     int64  `json:"org_id"`
	ModelName string `json:"model_name"`
	GrantedBy *int64 `json:"granted_by"`
	Source    string `json:"source"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (OrgModelGrant) TableName() string { return "org_model_grants" }

// QuotaGrant 额度拨备流水（审计：谁在何时给谁加了多少限额）
type QuotaGrant struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	SubjectType string `json:"subject_type"` // org / user
	SubjectID   int64  `json:"subject_id"`
	Amount      int64  `json:"amount"`
	Remark      string `json:"remark"`
	OperatorID  *int64 `json:"operator_id"`
	CreatedAt   int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (QuotaGrant) TableName() string { return "quota_grants" }
