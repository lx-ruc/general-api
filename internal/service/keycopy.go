package service

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/crypto"
	"token-gateway/internal/httpx"
)

// 密钥打码展示与复制完整密钥（创建时显示一次，之后列表只给打码串，
// 复制按钮走专用端点解出完整明文直接进剪贴板，界面永不回显）。

// KeyMaskedForList 列表打码串：有加密存档 → 解出完整明文统一打码（前 6+…+后 4）；
// 无存档（功能上线前存量）→ 仅前缀 + "****"（前端复制按钮置灰并提示重建）。
// 解密失败同样退回前缀形态（aes_key 变更后旧密文不可解，不能让整页列表 500）
func KeyMaskedForList(cipher *crypto.Cipher, enc, prefix string) string {
	if enc != "" && cipher != nil {
		if plain, err := cipher.Decrypt(enc); err == nil && plain != "" {
			return MaskKey(plain)
		}
	}
	if prefix == "" {
		return "****"
	}
	return prefix + "****"
}

// errKeyLegacyCopy 存量密钥（无加密存档）不可复制的统一文案
const errKeyLegacyCopy = "此密钥创建于功能上线前，无法复制，请删除重建"

// CopyKeyPayload 复制端点公共负载：按属主条件查出 enc 行 → 解密 → 回完整明文。
// 三种失败各自成义：行不存在=404；存量无存档=400（前端引导重建）；解密失败=500（aes_key 变更）。
// 每次成功取明文都 slog 留痕（audit_logs 由 Audit 中间件按请求记；actor 为发起人 uid）。
func CopyKeyPayload(c *gin.Context, db *gorm.DB, cipher *crypto.Cipher, what string,
	actor int64, query string, args ...any) {
	if cipher == nil { // 生产装配不会出现（main 启动即建）；防御测试/异常装配走明文透传
		cipher, _ = crypto.NewCipher("")
	}
	var row struct {
		ID  int64
		Enc string
	}
	if err := db.Raw(query, args...).Scan(&row).Error; err != nil || row.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, what+"不存在")
		return
	}
	if row.Enc == "" {
		httpx.Fail(c, http.StatusBadRequest, errKeyLegacyCopy)
		return
	}
	plain, err := cipher.Decrypt(row.Enc)
	if err != nil || plain == "" {
		slog.Error("密钥复制解密失败（aes_key 是否变更？）", "what", what, "id", row.ID, "err", err)
		httpx.Fail(c, http.StatusInternalServerError, "密钥解密失败，请联系系统管理员")
		return
	}
	slog.Info("密钥明文复制", "what", what, "id", row.ID, "actor_uid", actor)
	httpx.OK(c, gin.H{"key": plain})
}
