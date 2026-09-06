package model

// Org 公司（组织）
type Org struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	Name      string `json:"name"`
	Remark    string `json:"remark"`
	QuotaLimit int64 `json:"quota_limit"` // 点；平台分配的总限额
	QuotaUsed int64  `json:"quota_used"`  // 点；全公司实际消耗
	Status    int    `json:"status"`      // 1启用 0停用
	CreatedAt int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Org) TableName() string { return "orgs" }
