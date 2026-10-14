package service

import (
	"encoding/base64"
	"log/slog"

	"gorm.io/gorm"

	"token-gateway/internal/crypto"
)

// ReencryptPlaintextKeys 存量明文密钥原地加密（幂等，每次启动对账，常态零变更）。
//
// 背景：aes_key 留空时 Cipher 是明文透传，渠道 Key 一直以明文落库；一旦配置
// aes_key，这些行解密必失败（数据面全部渠道 DecryptFailed、密钥复制 500）。
// 此函数在启用加密后的首次启动把明文行原地收敛为密文，使「启用加密」与部署原子。
//
// 判定规则（防 aes_key 轮换误伤）：
//   - 解密成功 → 已是密文，跳过
//   - 解密失败且不形似密文 → 明文，原地加密
//   - 解密失败但形似密文 → 疑似旧密钥加密的密文（aes_key 被更换），跳过并告警；
//     自动再加密会把数据永久变成不可恢复的垃圾，必须人工用旧 key 解密回灌
func ReencryptPlaintextKeys(db *gorm.DB, c *crypto.Cipher) {
	if !c.Enabled() {
		return
	}
	// (表, 列)：渠道侧两列是启用加密前的主要存量；api_keys/access_tokens 兜底
	// 曾以空 aes_key 运行过又补配 key 的部署
	targets := []struct{ table, col string }{
		{"channels", "upstream_key_enc"},
		{"channel_keys", "key_enc"},
		{"api_keys", "key_enc"},
		{"access_tokens", "token_enc"},
	}
	for _, t := range targets {
		var rows []struct {
			ID  int64
			Enc string
		}
		if err := db.Raw("SELECT id, " + t.col + " AS enc FROM " + t.table +
			" WHERE " + t.col + " != ''").Scan(&rows).Error; err != nil {
			slog.Error("密钥加密对账读取失败", "table", t.table, "err", err)
			continue
		}
		reencrypted, skipped := 0, 0
		for _, r := range rows {
			if _, err := c.Decrypt(r.Enc); err == nil {
				continue // 已是密文
			}
			if looksLikeCiphertext(r.Enc) {
				skipped++
				slog.Error("疑似旧密钥密文，跳过自动加密（aes_key 被更换？需人工用旧 key 解密回灌）",
					"table", t.table, "id", r.ID)
				continue
			}
			enc, err := c.Encrypt(r.Enc)
			if err != nil {
				slog.Error("密钥加密失败", "table", t.table, "id", r.ID, "err", err)
				continue
			}
			if err := db.Exec("UPDATE "+t.table+" SET "+t.col+" = ? WHERE id = ?",
				enc, r.ID).Error; err != nil {
				slog.Error("密钥加密写回失败", "table", t.table, "id", r.ID, "err", err)
				continue
			}
			reencrypted++
		}
		if reencrypted > 0 || skipped > 0 {
			slog.Info("存量密钥加密对账完成", "table", t.table,
				"rows", len(rows), "reencrypted", reencrypted, "skipped_suspect", skipped)
		}
	}
}

// looksLikeCiphertext 形似密文：合法标准 base64 且解码 ≥ 28 字节
// （GCM nonce 12 + tag 16 的最小密文长度）。厂商明文 key 含 '-"/_' 等非 base64
// 字符或长度不足，按明文处理
func looksLikeCiphertext(s string) bool {
	raw, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(raw) >= 28
}
