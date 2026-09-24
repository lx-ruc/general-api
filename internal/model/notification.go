package model

// Notification 站内通知：需要系统管理员处理的运营事件（如 Key 配额冷却）。
// fan-out 到每个管理员一行，已读状态各自独立；payload 为 JSON 文本（含渠道/Key 定位
// 信息，绝不放密钥明文——只放打码形式），前端按 type 解析并给出就地处理动作
type Notification struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Type      string `json:"type"`      // key_quota_cooling
	Title     string `json:"title"`
	Body      string `json:"body"`
	Payload   string `json:"payload"`   // JSON：{"channel_id":..,"key_id":..,"channel_name":..,"key_masked":..}
	ReadAt    int64  `json:"read_at"`   // 0=未读
	CreatedAt int64  `json:"created_at"`
}
