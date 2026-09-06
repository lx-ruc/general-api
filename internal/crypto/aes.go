package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Cipher AES-256-GCM 加密器，key 为 nil 时明文透传（降低首次部署门槛）
type Cipher struct {
	key []byte
}

// NewCipher keyB64 为空返回禁用态；否则必须是 32 字节 base64
func NewCipher(keyB64 string) (*Cipher, error) {
	if keyB64 == "" {
		return &Cipher{}, nil
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("aes_key base64 decode: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("aes_key must be 32 bytes after base64 decode, got %d", len(key))
	}
	return &Cipher{key: key}, nil
}

func (c *Cipher) Enabled() bool { return c.key != nil }

// Encrypt 返回 base64(nonce + ciphertext)；未启用时原样返回
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if !c.Enabled() {
		return plaintext, nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 解密 Encrypt 的输出；未启用时原样返回
func (c *Cipher) Decrypt(ciphertext string) (string, error) {
	if !c.Enabled() {
		return ciphertext, nil
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}
	return string(plain), nil
}

// RandomKeyB64 生成一个 32 字节随机 key 的 base64（用于生成配置里的 aes_key）
func RandomKeyB64() string {
	b := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, b)
	return base64.StdEncoding.EncodeToString(b)
}
