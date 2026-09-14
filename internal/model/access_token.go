package model

// AccessToken 管理面访问令牌（程序化对接管理 API 用：开户/授权/查用量等）
// 复刻 api_keys 模式：明文不落库（SHA-256 唯一索引 O(1) 查找，仅创建时显示一次）；
// 权限 = 属主用户权限（走同一 RBAC / org 隔离链路）；吊销即失效（status=0）
type AccessToken struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`        // 备注名（如 "CI 集成"）
	TokenHash  string `json:"-"`           // SHA-256 hex，唯一索引
	Prefix     string `json:"prefix"`      // 打码前缀（tgp_ab12cd34…），列表辨认用
	Status     int    `json:"status"`      // 1=有效 0=已吊销
	ExpiresAt  int64  `json:"expires_at"`  // 0=永不过期
	LastUsedAt int64  `json:"last_used_at"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}
