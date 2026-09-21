package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "unit-test-secret"

// 正常往返：载荷/签发者/过期齐全，指针字段（org_id）与非指针字段都原样到达
func TestTokenRoundtrip(t *testing.T) {
	oid := int64(7)
	tok, err := GenerateToken(testSecret, time.Hour, 42, "member", &oid, "saltfingerprint")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c, err := ParseToken(testSecret, tok)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if c.UID != 42 || c.Role != "member" || c.OrgID == nil || *c.OrgID != 7 || c.Ver != "saltfingerprint" {
		t.Fatalf("载荷不符: %+v", c)
	}
	if c.Issuer != "token-gateway" {
		t.Fatalf("签发者不符: %q", c.Issuer)
	}
	if c.ExpiresAt == nil || c.ExpiresAt.Time.Before(time.Now()) {
		t.Fatalf("过期时间异常: %+v", c.ExpiresAt)
	}
}

// 平台管理员（org_id 为 nil）往返：字段省略不误读
func TestTokenRoundtripNoOrg(t *testing.T) {
	tok, err := GenerateToken(testSecret, time.Hour, 1, "platform_admin", nil, "")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c, err := ParseToken(testSecret, tok)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if c.OrgID != nil {
		t.Fatalf("org_id 应为 nil，得 %v", *c.OrgID)
	}
	if c.Ver != "" {
		t.Fatalf("ver 应为空，得 %q", c.Ver)
	}
}

// 过期、错密钥、篡改载荷、乱码、空串一律拒绝
func TestParseTokenRejects(t *testing.T) {
	oid := int64(3)
	tok, _ := GenerateToken(testSecret, time.Hour, 9, "member", &oid, "v")

	expired, _ := GenerateToken(testSecret, -time.Minute, 9, "member", &oid, "v")

	// 篡改：在 payload 段追加一个字符（签名必然失配）
	parts := strings.Split(tok, ".")
	tampered := parts[0] + "." + parts[1] + "x" + "." + parts[2]

	cases := []struct {
		name  string
		token string
		sec   string
	}{
		{"过期", expired, testSecret},
		{"错密钥", tok, "another-secret"},
		{"篡改载荷", tampered, testSecret},
		{"乱码", "not.a.jwt", testSecret},
		{"空串", "", testSecret},
		{"只有两段", parts[0] + "." + parts[1], testSecret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseToken(c.sec, c.token); err == nil {
				t.Fatalf("%s 的 token 不应解析通过", c.name)
			}
		})
	}
}

// alg=none 与非 HMAC 算法（RS/ES 混淆）：keyfunc 只认 HMAC，一律拒绝
func TestParseTokenRejectsAlgConfusion(t *testing.T) {
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	headerNone := b64([]byte(`{"alg":"none","typ":"JWT"}`))
	headerES := b64([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payload := b64([]byte(`{"uid":1,"role":"platform_admin","exp":` +
		fmt.Sprint(time.Now().Add(time.Hour).Unix()) + `}`))
	claims := &Claims{UID: 1, Role: "platform_admin"}

	for _, tc := range []struct {
		name string
		tok  string
	}{
		{"alg=none 无签名", headerNone + "." + payload + "."},
		{"alg=ES256 伪造签名", headerES + "." + payload + "." + b64([]byte("fakesig"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseToken(testSecret, tc.tok); err == nil {
				t.Fatal("非 HMAC 算法的 token 不应解析通过")
			}
		})
	}
	_ = claims
}

// SessionVer：同一哈希指纹稳定；不同哈希（新盐）指纹不同；短哈希返回空不 panic
func TestSessionVer(t *testing.T) {
	h1, err := HashPassword("pass-one")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	h2, err := HashPassword("pass-two")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	v1, v1Again := SessionVer(h1), SessionVer(h1)
	if v1 == "" || len(v1) != 12 {
		t.Fatalf("指纹应为 12 位盐段，得 %q", v1)
	}
	if v1 != v1Again {
		t.Fatal("同一哈希指纹应稳定")
	}
	if v2 := SessionVer(h2); v2 == "" || v2 == v1 {
		t.Fatalf("不同哈希指纹应不同：%q vs %q", v1, v2)
	}
	// bcrypt 前缀 "$2a$10$" 占 7 位 + 22 位盐，共 29 位起才可切
	if got := SessionVer("$2a$10$short"); got != "" {
		t.Fatalf("短哈希应返回空，得 %q", got)
	}
	if got := SessionVer(""); got != "" {
		t.Fatalf("空哈希应返回空，得 %q", got)
	}
}

// SessionVer 指纹变化与 JWT 失配路径联动：改密后旧 token 的 ver 对不上新哈希
func TestSessionVerRotation(t *testing.T) {
	h1, _ := HashPassword("old-pass")
	h2, _ := HashPassword("new-pass")
	oid := int64(5)
	oldTok, _ := GenerateToken(testSecret, time.Hour, 10, "member", &oid, SessionVer(h1))
	c, err := ParseToken(testSecret, oldTok)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if SessionVer(h2) == c.Ver {
		t.Fatal("改密后的指纹不应与旧会话一致")
	}
}

// 纵深防御：缺 exp（永不过期）或签发者非本服务的 token，即使签名正确也拒绝
func TestParseTokenRequiresRegisteredClaims(t *testing.T) {
	mk := func(claims jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		return s
	}
	future := time.Now().Add(time.Hour).Unix()
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims
	}{
		{"缺 exp", jwt.MapClaims{"uid": 1, "role": "member", "iss": "token-gateway"}},
		{"签发者不符", jwt.MapClaims{"uid": 1, "role": "member", "iss": "other-service", "exp": future}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseToken(testSecret, mk(tc.claims)); err == nil {
				t.Fatalf("%s 的 token 不应放行", tc.name)
			}
		})
	}
}
