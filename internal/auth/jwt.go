package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 管理台 JWT 载荷
type Claims struct {
	UID   int64  `json:"uid"`
	Role  string `json:"role"`
	OrgID *int64 `json:"org_id,omitempty"`
	Ver   string `json:"ver,omitempty"` // 会话指纹（密码哈希盐段）：密码一改旧会话即时失效；空=升级前签发的存量 token
	jwt.RegisteredClaims
}

// SessionVer 从密码哈希派生会话指纹：取 bcrypt 盐段（哈希位 7..19，盐随每次加密随机）。
// 改密码/重置密码 → 新哈希新盐 → 旧指纹失配。哈希格式异常返回空（不启用钉扎，等同存量 token）。
func SessionVer(passwordHash string) string {
	if len(passwordHash) < 29 { // "$2a$10$" 7 位 + 22 位盐
		return ""
	}
	return passwordHash[7:19]
}

func GenerateToken(secret string, ttl time.Duration, uid int64, role string, orgID *int64, ver string) (string, error) {
	now := time.Now()
	claims := Claims{
		UID:   uid,
		Role:  role,
		OrgID: orgID,
		Ver:   ver,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "token-gateway",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

func ParseToken(secret, tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	},
		// 纵深防御：即使持密钥者手造的 token，缺 exp（永不过期）或签发者
		// 不是本服务（共用密钥的其他系统）也一律拒绝
		jwt.WithExpirationRequired(), jwt.WithIssuer("token-gateway"))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}
	return claims, nil
}
