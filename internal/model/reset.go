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

// EmailBindCode 邮箱绑定/换绑验证码（user 维度：每次发码作废该用户旧码，验证锚定
// 最新一行）；Email 为发码时的目标邮箱快照，确认时比对防「拿 A 邮箱的码绑 B 邮箱」
type EmailBindCode struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	Code      string `json:"-"`
	ExpireAt  int64  `json:"expire_at"`
	SentAt    int64  `json:"sent_at"`
	Attempts  int    `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
}

// TableName 显式声明以防歧义
func (EmailBindCode) TableName() string { return "email_bind_codes" }
