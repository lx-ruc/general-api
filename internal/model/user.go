package model

const (
	RolePlatformAdmin = "platform_admin"
	RoleOrgAdmin      = "org_admin"
	RoleMember        = "member"
)

// User 三级账号同表，role 区分；org_id 为 NULL 即平台管理员
type User struct {
	ID           int64  `gorm:"primaryKey" json:"id"`
	OrgID        *int64 `json:"org_id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"` // gorm 自动映射 password_hash 列；仅不出现在 JSON
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	QuotaLimit    *int64 `json:"quota_limit"`  // NULL=不限额（仅 member 有意义）
	QuotaUsed     int64  `json:"quota_used"`
	MonthlyQuota  int64  `json:"monthly_quota"` // 单月消费上限（点；0=不限）
	MonthlyCost   int64  `json:"monthly_cost"`  // 当前账期累计（monthly_period 有效时）
	MonthlyPeriod string `json:"monthly_period"`
	AlertLevels   string `json:"alert_levels"` // 个人预警阈值 JSON（[] = 关闭；不限额者不参与）
	AlertLevel   int    `json:"alert_level"`  // 当前已达档位（边沿状态机）
	AlertSince   int64  `json:"alert_since"`
	Status       int    `json:"status"`
	LastLoginAt  *int64 `json:"last_login_at"`
	CreatedAt    int64  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    int64  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (User) TableName() string { return "users" }
