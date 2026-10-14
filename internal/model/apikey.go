package model

// APIKey 鉴权只存哈希；完整明文 AES-GCM 加密另存（key_enc，仅「复制完整密钥」端点解出），
// 界面永不回显明文——创建时显示一次，之后一律打码
type APIKey struct {
	ID           int64  `gorm:"primaryKey" json:"id"`
	OrgID        int64  `json:"org_id"`
	UserID       int64  `json:"user_id"`
	Name         string `json:"name"`
	KeyPrefix    string `json:"key_prefix"` // 前 12 字符，仅用于展示/检索
	KeyHash      string `json:"-"`          // SHA-256 hex，唯一索引
	KeyEnc       string `json:"-"`          // 完整明文密文（''=功能上线前存量，不可复制）；aes_key 空=明文透传
	Status       int    `json:"status"`
	ExpiredAt    *int64 `json:"expired_at"`
	LastUsedAt   *int64 `json:"last_used_at"`
	CostCenterID *int64 `json:"cost_center_id"` // 归集中心；NULL=未归集
	CreatedAt    int64  `gorm:"autoCreateTime" json:"created_at"`
}

func (APIKey) TableName() string { return "api_keys" }
