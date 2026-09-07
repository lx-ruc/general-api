package model

// CostCenter 成本中心：org 内受控归集词表（"为哪个项目花的"）
// status 0=归档；永不硬删——历史 usage_logs 快照与归档中心均保留，归档后仅从下拉消失
type CostCenter struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	OrgID     int64  `json:"org_id"`
	Name      string `json:"name"`
	Status    int    `json:"status"`
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (CostCenter) TableName() string { return "cost_centers" }
