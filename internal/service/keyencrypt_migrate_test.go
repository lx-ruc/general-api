package service

import (
	"encoding/base64"
	"testing"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
)

func newKeyEncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	return gdb
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("执行失败 %s: %v", sql, err)
	}
}

func scanString(t *testing.T, db *gorm.DB, sql string) string {
	t.Helper()
	var got string
	if err := db.Raw(sql).Scan(&got).Error; err != nil {
		t.Fatalf("查询失败 %s: %v", sql, err)
	}
	return got
}

// 场景一：aes_key 从空 → 配置后，存量明文密钥原地变密文且可解密还原；
// 已密文 / 空值 / 疑似旧密钥密文 三类行不受影响；整过程幂等
func TestReencryptPlaintextKeys(t *testing.T) {
	db := newKeyEncTestDB(t)
	cipher, err := crypto.NewCipher(crypto.RandomKeyB64())
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	oldCipher, _ := crypto.NewCipher(crypto.RandomKeyB64())

	const (
		plainKey   = "sk-plain-legacy-key-0001" // 含 '-'（base64 字母表外）→ 判明文
		cipherKey  = "sk-already-encrypted-002" // 以当前 cipher 预加密 → 判已密文
		rotatedKey = "sk-rotated-old-key-003"   // 以另一把 key 加密 → 判疑似旧密文
	)
	encCur, _ := cipher.Encrypt(cipherKey)
	encOld, _ := oldCipher.Encrypt(rotatedKey)

	// 渠道三行（明文/已密文/疑似旧密文）+ 一行空值；另三张表各放一行明文验证同构收敛
	mustExec(t, db, `INSERT INTO channels (id, name, vendor, base_url, path, weight, priority, status,
		upstream_key_enc, created_at, updated_at) VALUES
		(1, 'c1', 'v', 'https://a', '/v1', 1, 0, 1, ?, 0, 0),
		(2, 'c2', 'v', 'https://b', '/v1', 1, 0, 1, ?, 0, 0),
		(3, 'c3', 'v', 'https://c', '/v1', 1, 0, 1, ?, 0, 0),
		(4, 'c4', 'v', 'https://d', '/v1', 1, 0, 1, '', 0, 0)`,
		plainKey, encCur, encOld)
	// 父行（api_keys 有 org/user 外键）
	mustExec(t, db, `INSERT INTO orgs (id, name, created_at, updated_at) VALUES (1, 'o1', 0, 0)`)
	mustExec(t, db, `INSERT INTO users (id, org_id, username, password_hash, role, created_at, updated_at)
		VALUES (1, 1, 'u1', 'x', 'member', 0, 0)`)

	mustExec(t, db, `INSERT INTO channel_keys (id, channel_id, key_enc, weight, status, created_at, updated_at)
		VALUES (1, 1, ?, 1, 1, 0, 0)`, plainKey+"-pool")
	mustExec(t, db, `INSERT INTO api_keys (id, org_id, user_id, name, key_prefix, key_hash, key_enc, status, created_at)
		VALUES (1, 1, 1, 'k', 'sk-abcd', 'x', ?, 1, 0)`, plainKey+"-api")
	mustExec(t, db, `INSERT INTO access_tokens (id, user_id, name, token_hash, token_enc, status, created_at)
		VALUES (1, 1, 't', 'x', ?, 1, 0)`, plainKey+"-token")

	ReencryptPlaintextKeys(db, cipher)

	// 明文行 → 密文（值不再是明文，且可解密还原）
	for _, tc := range []struct{ table, col, want string }{
		{"channels", "upstream_key_enc", plainKey},
		{"channel_keys", "key_enc", plainKey + "-pool"},
		{"api_keys", "key_enc", plainKey + "-api"},
		{"access_tokens", "token_enc", plainKey + "-token"},
	} {
		got := scanString(t, db, "SELECT "+tc.col+" FROM "+tc.table+" WHERE id = 1")
		if got == tc.want {
			t.Fatalf("%s.%s 仍是明文，未被加密", tc.table, tc.col)
		}
		plain, err := cipher.Decrypt(got)
		if err != nil || plain != tc.want {
			t.Fatalf("%s.%s 加密后无法解密还原: err=%v plain=%q", tc.table, tc.col, err, plain)
		}
	}

	// 已密文行逐字节不动；疑似旧密钥密文不动（轮换场景自动再加密会毁数据）；空值保持空
	if got := scanString(t, db, "SELECT upstream_key_enc FROM channels WHERE id = 2"); got != encCur {
		t.Fatal("已加密行被改动")
	}
	if got := scanString(t, db, "SELECT upstream_key_enc FROM channels WHERE id = 3"); got != encOld {
		t.Fatal("疑似旧密钥密文被误加密")
	}
	if got := scanString(t, db, "SELECT upstream_key_enc FROM channels WHERE id = 4"); got != "" {
		t.Fatal("空值行被改动")
	}

	// 幂等：二次对账不破坏密文
	ReencryptPlaintextKeys(db, cipher)
	got := scanString(t, db, "SELECT upstream_key_enc FROM channels WHERE id = 1")
	if plain, err := cipher.Decrypt(got); err != nil || plain != plainKey {
		t.Fatalf("二次对账破坏了密文: err=%v plain=%q", err, plain)
	}
}

// 场景二：aes_key 未配置（Cipher 禁用）时为彻底 no-op——明文原样保留
func TestReencryptDisabledCipherNoop(t *testing.T) {
	db := newKeyEncTestDB(t)
	disabled, _ := crypto.NewCipher("")
	mustExec(t, db, `INSERT INTO channels (id, name, vendor, base_url, path, weight, priority, status,
		upstream_key_enc, created_at, updated_at) VALUES
		(1, 'c1', 'v', 'https://a', '/v1', 1, 0, 1, 'sk-plain', 0, 0)`)
	ReencryptPlaintextKeys(db, disabled)
	if got := scanString(t, db, "SELECT upstream_key_enc FROM channels WHERE id = 1"); got != "sk-plain" {
		t.Fatalf("禁用加密器不应改动数据, got %q", got)
	}
}

// looksLikeCiphertext：真密文判真；含非 base64 字符或解码不足 28 字节的明文判假
func TestLooksLikeCiphertext(t *testing.T) {
	cipher, _ := crypto.NewCipher(crypto.RandomKeyB64())
	enc, _ := cipher.Encrypt("sk-something-secret-value")
	if !looksLikeCiphertext(enc) {
		t.Fatal("真密文应形似密文")
	}
	for _, s := range []string{
		"sk-abc123",                                       // 含 '-'（标准 base64 字母表外）
		"shortbutbase64!@_",                               // 非 base64 字符
		base64.StdEncoding.EncodeToString([]byte("tiny")), // 合法 base64 但解码 < 28 字节
		"",                                                // 空串
	} {
		if looksLikeCiphertext(s) {
			t.Fatalf("%q 不应判为密文", s)
		}
	}
}
