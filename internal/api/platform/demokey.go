package platform

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// 对外演示体验密钥：管理员配置一枚可直接交给来访客户调 /v1 的真实 API key。
// 与「顶栏在线体验」（JWT+合成身份）互不影响——这里走完整数据面：
// api_keys 鉴权、模型授权、额度预检、计量计费全部照常，消耗记在专用体验账号名下。
//
// 账号模型：专用客户「在线体验」+ 子账号 online_demo（个人不限额），
// 体验总额度由客户 quota_limit 承担（走 AddOrgQuota，保持 Σgrants 不变量）。
// 密钥明文存 settings（demo.api_key）：演示密钥本就要反复发给访客，
// 管理员须能随时查看/复制——与普通密钥「只显示一次」的约定不同，此处是有意为之。

const (
	settingDemoUserID = "demo.user_id"
	settingDemoKey    = "demo.api_key"
	settingDemoKeyID  = "demo.key_id"

	demoOrgName     = "在线体验"
	demoOrgRemark   = "对外演示专用：体验密钥的消耗记在此客户名下（系统自动开通）"
	demoUsername    = "online_demo"
	demoUserDisplay = "在线体验访客"
	demoKeyName     = "在线体验"
)

// setSettingTx 事务内 upsert 设置项
func setSettingTx(tx *gorm.DB, key, value string) error {
	return tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value).Error
}

// getSettingInt 读整型设置；缺行/空值返回 0（NULL 防御同 bug #14：COALESCE 兜底）
func getSettingInt(db *gorm.DB, key string) int64 {
	var v int64
	_ = db.Raw("SELECT COALESCE(CAST(value AS INTEGER), 0) FROM settings WHERE key = ?", key).Scan(&v).Error
	return v
}

// getSettingStr 读字符串设置
func getSettingStr(db *gorm.DB, key string) string {
	var v string
	_ = db.Raw("SELECT COALESCE(value, '') FROM settings WHERE key = ?", key).Scan(&v).Error
	return v
}

// ensureDemoAccount 幂等开通体验账号：settings 记 user_id 锚点；
// 半途残留（有客户无锚点/有账号无锚点）按名字认领，无法认领才报错
func (h *Handler) ensureDemoAccount() (uid, orgID int64, err error) {
	if uid = getSettingInt(h.DB, settingDemoUserID); uid > 0 {
		if err := h.DB.Raw("SELECT COALESCE(org_id, 0) FROM users WHERE id = ?", uid).Scan(&orgID).Error; err == nil && orgID > 0 {
			return uid, orgID, nil
		}
	}

	err = h.DB.Transaction(func(tx *gorm.DB) error {
		// 1) 体验客户：按名认领或新建（alert_levels 零值省略走列默认 [80]）
		var oid int64
		if err := tx.Raw("SELECT id FROM orgs WHERE name = ?", demoOrgName).Scan(&oid).Error; err != nil {
			return err
		}
		if oid == 0 {
			org := model.Org{Name: demoOrgName, Remark: demoOrgRemark, Status: 1}
			if err := tx.Create(&org).Error; err != nil {
				return err
			}
			oid = org.ID
		}
		orgID = oid

		// 2) 体验子账号：按名认领（须挂在本客户下，防同名占用误认领）或新建
		var uid2 int64
		if err := tx.Raw("SELECT id FROM users WHERE username = ?", demoUsername).Scan(&uid2).Error; err != nil {
			return err
		}
		if uid2 > 0 {
			var owner int64
			if err := tx.Raw("SELECT COALESCE(org_id, 0) FROM users WHERE id = ?", uid2).Scan(&owner).Error; err != nil {
				return err
			}
			if owner != oid {
				return errDemoUsernameTaken
			}
		} else {
			randomPwd, _, _, err := auth.GenerateAPIKey() // 24 字节随机 hex 作初始密码，仅占位（体验账号不用于登录）
			if err != nil {
				return err
			}
			hash, err := auth.HashPassword(randomPwd)
			if err != nil {
				return err
			}
			u := model.User{
				OrgID: &oid, Username: demoUsername, PasswordHash: hash,
				DisplayName: demoUserDisplay, Role: "member", Status: 1, // 个人不限额（quota_limit NULL）
			}
			if err := tx.Create(&u).Error; err != nil {
				return err
			}
			uid2 = u.ID
		}
		uid = uid2
		return setSettingTx(tx, settingDemoUserID, strconv.FormatInt(uid2, 10))
	})
	if err != nil {
		return 0, 0, err
	}
	return uid, orgID, nil
}

// errDemoUsernameTaken 体验账号用户名被无关账号占用
var errDemoUsernameTaken = gorm.ErrDuplicatedKey

// demoSnapshot 当前配置全量视图
func (h *Handler) demoSnapshot(c *gin.Context, uid, orgID int64) {
	keyID := getSettingInt(h.DB, settingDemoKeyID)
	var key model.APIKey
	if keyID > 0 {
		if err := h.DB.Raw("SELECT id, key_prefix, status, expired_at, created_at FROM api_keys WHERE id = ?",
			keyID).Scan(&key).Error; err != nil || key.ID == 0 {
			keyID = 0
		}
	}
	var org model.Org
	if err := h.DB.Raw("SELECT id, name, quota_limit, quota_used FROM orgs WHERE id = ?", orgID).Scan(&org).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询体验客户失败")
		return
	}
	var used int64
	_ = h.DB.Raw("SELECT quota_used FROM users WHERE id = ?", uid).Scan(&used).Error
	var models []string
	_ = h.DB.Raw("SELECT model_name FROM user_model_grants WHERE user_id = ? ORDER BY model_name", uid).Scan(&models).Error
	if models == nil {
		models = []string{}
	}
	expiresAt := int64(0)
	if key.ExpiredAt != nil {
		expiresAt = *key.ExpiredAt
	}
	httpx.OK(c, gin.H{
		"provisioned": keyID > 0,
		"key":         getSettingStr(h.DB, settingDemoKey), // 演示密钥明文：管理员可反复查看/复制
		"key_prefix":  key.KeyPrefix,
		"key_id":      keyID,
		"enabled":     key.Status == 1,
		"expires_at":  expiresAt, // 0=永久
		"user": gin.H{
			"id": uid, "username": demoUsername,
			"quota_limit": nil, "quota_used": used, // 个人不限额
		},
		"org": gin.H{
			"id": orgID, "name": org.Name,
			"quota_limit": org.QuotaLimit, "quota_used": org.QuotaUsed,
		},
		"models": models,
	})
}

// GetDemoKey GET /api/platform/demo-key 体验密钥配置视图
func (h *Handler) GetDemoKey(c *gin.Context) {
	uid, orgID, err := h.ensureDemoAccount()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "体验账号开通失败："+errDemoMsg(err))
		return
	}
	h.demoSnapshot(c, uid, orgID)
}

// errDemoMsg 面向管理员的错误翻译
func errDemoMsg(err error) string {
	if err == errDemoUsernameTaken {
		return "用户名 online_demo 已被无关账号占用，请处理后重试"
	}
	return err.Error()
}

// ConfigureDemoKey PUT /api/platform/demo-key 保存配置（首次调用即开通账号）
func (h *Handler) ConfigureDemoKey(c *gin.Context) {
	var req struct {
		Enabled     *bool    `json:"enabled"`              // 密钥启停（false=立即失效）
		ExpiresAt   *int64   `json:"expires_at"`           // unix 秒；0=永久
		QuotaPoints *int64   `json:"quota_points"`         // 体验总额度（点）：体验客户 quota_limit 目标值
		Models      []string `json:"models"`               // 授权模型全量替换；nil=不修改
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	operator := middleware.GetUID(c)

	uid, orgID, err := h.ensureDemoAccount()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "体验账号开通失败："+errDemoMsg(err))
		return
	}

	// 1) 体验总额度：目标值变更为准（走 AddOrgQuota 保持 Σgrants == quota_limit）
	if req.QuotaPoints != nil {
		if *req.QuotaPoints < 0 {
			httpx.Fail(c, http.StatusBadRequest, "体验总额度不能为负")
			return
		}
		var cur, used int64
		_ = h.DB.Raw("SELECT quota_limit FROM orgs WHERE id = ?", orgID).Scan(&cur).Error
		_ = h.DB.Raw("SELECT quota_used FROM orgs WHERE id = ?", orgID).Scan(&used).Error
		if *req.QuotaPoints < used {
			httpx.Fail(c, http.StatusBadRequest,
				"体验总额度不能低于已消耗（"+strconv.FormatInt(used, 10)+" 点）")
			return
		}
		if diff := *req.QuotaPoints - cur; diff != 0 {
			if err := service.AddOrgQuota(h.DB, orgID, diff, operator, "在线体验额度设置"); err != nil {
				httpx.Fail(c, http.StatusInternalServerError, "额度调整失败："+err.Error())
				return
			}
		}
	}

	// 2）模型授权：全量替换（与子账号授权同范式）
	if req.Models != nil {
		dedup := map[string]bool{}
		list := make([]string, 0, len(req.Models))
		for _, name := range req.Models {
			if name == "" || dedup[name] {
				continue
			}
			var mc int64
			_ = h.DB.Model(&model.Model{}).Where("name = ? AND status = 1", name).Count(&mc).Error
			if mc == 0 {
				httpx.Fail(c, http.StatusBadRequest, "模型不存在或未启用: "+name)
				return
			}
			dedup[name] = true
			list = append(list, name)
		}
		err := h.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("user_id = ?", uid).Delete(&model.UserModelGrant{}).Error; err != nil {
				return err
			}
			for _, name := range list {
				if err := tx.Create(&model.UserModelGrant{
					UserID: uid, ModelName: name, GrantedBy: &operator,
				}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "模型授权失败："+err.Error())
			return
		}
	}

	// 3) 密钥属性：有效期 / 启停
	if req.ExpiresAt != nil || req.Enabled != nil {
		sets := []string{}
		args := []any{}
		if req.ExpiresAt != nil {
			if *req.ExpiresAt < 0 {
				httpx.Fail(c, http.StatusBadRequest, "有效期不能为负")
				return
			}
			if *req.ExpiresAt == 0 {
				sets = append(sets, "expired_at = NULL") // 0=永久
			} else {
				sets = append(sets, "expired_at = ?")
				args = append(args, *req.ExpiresAt)
			}
		}
		if req.Enabled != nil {
			b := 0
			if *req.Enabled {
				b = 1
			}
			sets = append(sets, "status = ?")
			args = append(args, b)
		}
		keyID := getSettingInt(h.DB, settingDemoKeyID)
		args = append(args, keyID)
		if err := h.DB.Exec("UPDATE api_keys SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...).Error; err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "密钥更新失败")
			return
		}
	}

	// 保存后直接回完整视图（前端免二次拉取）
	h.demoSnapshot(c, uid, orgID)
}

// RotateDemoKey POST /api/platform/demo-key/rotate 轮换体验密钥
// （吊销体验账号全部旧密钥 + 生成新密钥；旧 key 立即 401）
func (h *Handler) RotateDemoKey(c *gin.Context) {
	uid, orgID, err := h.ensureDemoAccount()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "体验账号开通失败："+errDemoMsg(err))
		return
	}
	plain, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	// 新钥继承现钥的有效期：有效期是持久配置（模型授权/额度挂在账号上本就不丢），
	// 落在 key 行上，轮换若不带过来会静默丢失变永久——应急轮换恰恰最需要保留期限
	var inheritExp *int64
	var cur model.APIKey
	if keyID := getSettingInt(h.DB, settingDemoKeyID); keyID > 0 && h.DB.First(&cur, keyID).Error == nil {
		inheritExp = cur.ExpiredAt
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE api_keys SET status = 0 WHERE user_id = ? AND status = 1", uid).Error; err != nil {
			return err
		}
		k := model.APIKey{
			OrgID: orgID, UserID: uid, Name: demoKeyName,
			KeyPrefix: prefix, KeyHash: hash, Status: 1,
			ExpiredAt: inheritExp,
		}
		if err := tx.Create(&k).Error; err != nil {
			return err
		}
		if err := setSettingTx(tx, settingDemoKey, plain); err != nil {
			return err
		}
		return setSettingTx(tx, settingDemoKeyID, strconv.FormatInt(k.ID, 10))
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "轮换失败："+err.Error())
		return
	}
	httpx.OK(c, gin.H{"key": plain, "message": "已轮换，旧密钥立即失效"})
}
