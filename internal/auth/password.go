package auth

import (
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 10

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// DummyHash 用户不存在时做等耗时的哑比较：登录无论用户名是否存在都走一次 bcrypt，
// 抹平响应时差，防按响应时间探测用户名是否存在。
var DummyHash, _ = HashPassword("timing-equalizer-dummy")
