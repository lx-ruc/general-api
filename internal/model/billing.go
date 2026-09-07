package model

// PeriodBalance 月末余额快照：period 'YYYY-MM' 表示"该月末时点"的 org limit/used。
// 勾稽：期初[M] = snapshot[M-1]，期末[M] = snapshot[M]；used 只被结算单调累加，grants 不动 used。
type PeriodBalance struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	OrgID      int64  `json:"org_id"`
	Period     string `json:"period"`
	QuotaLimit int64  `json:"quota_limit"`
	QuotaUsed  int64  `json:"quota_used"`
	SnapshotAt int64  `json:"snapshot_at"`
	CreatedAt  int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt  int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (PeriodBalance) TableName() string { return "period_balances" }

// VendorBill 厂商账单手工录入（对账用）：与 usage_logs Σvendor_cost 按渠道比差异
type VendorBill struct {
	ID           int64  `gorm:"primaryKey" json:"id"`
	Period       string `json:"period"`
	ChannelID    int64  `json:"channel_id"`
	BilledPoints int64  `json:"billed_points"`
	Note         string `json:"note"`
	CreatedBy    *int64 `json:"created_by"`
	CreatedAt    int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (VendorBill) TableName() string { return "vendor_bills" }
