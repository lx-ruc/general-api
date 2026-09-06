package model

// UserModelGrant 员工模型白名单（公司管理员授权；无记录=无权限）
type UserModelGrant struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	UserID    int64  `json:"user_id"`
	ModelName string `json:"model_name"`
	GrantedBy *int64 `json:"granted_by"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (UserModelGrant) TableName() string { return "user_model_grants" }

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
