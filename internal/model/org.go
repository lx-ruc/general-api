package model

// Org 客户（组织）
type Org struct {
	ID                int64  `gorm:"primaryKey" json:"id"`
	Name              string `json:"name"`
	Remark            string `json:"remark"`
	ContactName       string `json:"contact_name"`  // 客户联系人
	ContactPhone      string `json:"contact_phone"` // 联系电话
	QuotaLimit        int64  `json:"quota_limit"`   // 点；平台分配的总限额
	QuotaUsed         int64  `json:"quota_used"`    // 点；全客户实际消耗
	MonthlyQuota      int64  `json:"monthly_quota"` // 单月消费上限（点；0=不限）
	MonthlyCost       int64  `json:"monthly_cost"`  // 当前账期累计（monthly_period 有效时）
	MonthlyPeriod     string `json:"monthly_period"`
	Status            int    `json:"status"`              // 1启用 0停用 2欠费停服（自动）
	RequireCostCenter int    `json:"require_cost_center"` // 1=新建 key 必须归集成本中心
	AlertLevels       string `json:"alert_levels"`        // 预警阈值升序 JSON（[] = 关闭）
	AlertLevel        int    `json:"alert_level"`         // 当前已达档位（边沿状态机）
	AlertSince        int64  `json:"alert_since"`         // 进入当前档位的时间
	CreatedAt         int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt         int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Org) TableName() string { return "orgs" }
