package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/model"
)

// 站内通知：需要系统管理员处理的事件落库（顶栏铃铛轮询展示），与邮件通知互补。
// 邮件适合「渠道禁用」这类低频严重事件；Key 配额冷却可能反复发生（同一把死 Key
// 每分钟被再探测一次），站内 + 节流才不刷屏。

// NotificationType 常量：前端按 type 渲染图标与就地动作
const (
	NotifyTypeKeyQuotaCooling = "key_quota_cooling"
)

// NotifyThrottle 同一 Key 两次配额冷却通知的最小间隔：死 Key 稳态下每 ~1 分钟被真实
// 流量/探测再触发一次配额 429，不节流会每分钟刷一条。设为变量便于测试缩短
var NotifyThrottle = 30 * time.Minute

// notifyLast 进程内节流表：scope → 上次发出时间（多实例为 per-node，与限流器口径一致）
var notifyLast sync.Map

// KeyQuotaPayload key_quota_cooling 通知的 JSON 附件：定位到渠道与 Key，
// key_masked 为打码形式（前 6 + … + 后 4），绝不放密钥明文/密文
type KeyQuotaPayload struct {
	ChannelID   int64  `json:"channel_id"`
	KeyID       int64  `json:"key_id"` // 0 = legacy 单 Key 模式（无池行，只能靠探测成功自愈）
	ChannelName string `json:"channel_name"`
	KeyMasked   string `json:"key_masked"`
	ErrCode     string `json:"err_code"` // 厂商侧错误码（SetLimitExceeded 等）
}

// MaskKey 打码展示：前 6 + … + 后 4（短 key 只露前 3）
func MaskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 12 {
		n := min(3, len(k))
		return k[:n] + "…"
	}
	return k[:6] + "…" + k[len(k)-4:]
}

// NotifyKeyQuotaCooling 给全部启用的系统管理员各发一条「Key 进入配额冷却」站内通知。
// 同一 scope（渠道+Key）在 NotifyThrottle 窗口内只发一次。同步写库（一次 SELECT ids +
// 一条批量 INSERT），调用方在数据面热路径上请用 `go` 异步调用。
// 返回是否真正发出（节流命中返回 false，不算错误）
func NotifyKeyQuotaCooling(db *gorm.DB, channelID, keyID int64, channelName, rawKey, errCode string) bool {
	scope := fmt.Sprintf("ck:%d:%d", channelID, keyID)
	now := time.Now()
	if last, ok := notifyLast.Load(scope); ok {
		if t, _ := last.(time.Time); now.Sub(t) < NotifyThrottle {
			return false
		}
	}
	notifyLast.Store(scope, now)

	payload, _ := json.Marshal(KeyQuotaPayload{
		ChannelID: channelID, KeyID: keyID, ChannelName: channelName,
		KeyMasked: MaskKey(rawKey), ErrCode: errCode,
	})
	body := fmt.Sprintf("渠道「%s」的 Key %s 因厂商侧配额耗尽（%s）进入冷却。"+
		"冷却到期由真实流量自动再探测，配额恢复后下一笔请求即成功；"+
		"等不及可在通知详情或渠道 Key 池里点「清除冷却」立即复用。",
		channelName, MaskKey(rawKey), errCode)

	var adminIDs []int64
	if err := db.Raw(`SELECT id FROM users WHERE role = 'platform_admin' AND status = 1`).Scan(&adminIDs).Error; err != nil {
		slog.Warn("站内通知扇出查询失败", "err", err)
		return false
	}
	if len(adminIDs) == 0 {
		return false
	}
	rows := make([]model.Notification, 0, len(adminIDs))
	for _, uid := range adminIDs {
		rows = append(rows, model.Notification{
			UserID: uid, Type: NotifyTypeKeyQuotaCooling,
			Title: fmt.Sprintf("Key 配额冷却：%s", channelName),
			Body:  body, Payload: string(payload), CreatedAt: now.Unix(),
		})
	}
	if err := db.Create(&rows).Error; err != nil {
		slog.Warn("站内通知写入失败", "err", err)
		return false
	}
	slog.Warn("Key 进入配额冷却，已站内通知系统管理员",
		"channel_id", channelID, "channel", channelName, "key_id", keyID, "code", errCode, "notified", len(rows))
	return true
}
