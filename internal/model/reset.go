package model

// PasswordResetToken 密码重置令牌：TokenHash 为明文 token 的 SHA-256 十六进制
// （明文只出现在邮件链接里，不落库）；UsedAt=0 表示未使用
type PasswordResetToken struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	TokenHash string `json:"token_hash"`
	ExpireAt  int64  `json:"expire_at"`
	UsedAt    int64  `json:"used_at"`
	CreatedAt int64  `json:"created_at"`
}

// TableName GORM 默认复数表名（password_reset_tokens）恰好正确，显式声明以防歧义
func (PasswordResetToken) TableName() string { return "password_reset_tokens" }
