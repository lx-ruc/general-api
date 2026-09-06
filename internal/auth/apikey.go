package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// GenerateAPIKey 生成 sk- 前缀密钥：sk- + 48 个十六进制字符（192-bit 随机）
// 返回 明文（仅此一次可见）、前缀（展示用）、SHA-256 哈希（落库）
func GenerateAPIKey() (plain, prefix, hash string, err error) {
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("rand: %w", err)
	}
	plain = "sk-" + hex.EncodeToString(b)
	prefix = plain[:12]
	return plain, prefix, HashAPIKey(plain), nil
}

// HashAPIKey SHA-256 hex。key 本身是 192-bit 高熵随机值，穷举不可行，
// 无盐才能建唯一索引做 O(1) 等值查找
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
