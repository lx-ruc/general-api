package model

// APIKey 只存哈希；明文仅创建时返回一次
type APIKey struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	OrgID      int64  `json:"org_id"`
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	KeyPrefix  string `json:"key_prefix"` // 前 12 字符，仅用于展示/检索
	KeyHash    string `json:"-"`          // SHA-256 hex，唯一索引
	Status     int    `json:"status"`
	ExpiredAt  *int64 `json:"expired_at"`
	LastUsedAt *int64 `json:"last_used_at"`
	CreatedAt  int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (APIKey) TableName() string { return "api_keys" }
