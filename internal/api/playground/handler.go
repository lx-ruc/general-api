package playground

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/gateway"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
)

// 在线体验：管理台顶栏对话框，JWT 鉴权后注入合成 KeyInfo 复用数据面完整编排
// （多 Key 池 / 排队削峰 / 熔断 / SSE 透传 / 计费结算全部与 /v1 一致）。
//
// 授权按角色收敛（与子账号白名单语义对齐，只放出"真正能调通"的模型）：
//   - 子账号        ：个人模型白名单 ∩ 可路由（启用模型 × 启用渠道）
//   - 客户管理员  ：本客户子账号授权并集 ∩ 可路由（客户已购能力的范畴）
//   - 系统管理员  ：全部可路由模型（运营自测，与渠道测试一致不计费）
//
// 计费：子账号/客户管理员走真实预检与结算（等同用自己的 key 调用）；
// 系统管理员无客户归属（UserID/OrgID=0），不预检、不计费，仅留计量日志。

// Handler 在线体验
type Handler struct {
	DB *gorm.DB
	GW *gateway.Handler
}

// NewHandler 构造（GW 为数据面编排，透传 /v1 同款链路）
func NewHandler(db *gorm.DB, gw *gateway.Handler) *Handler {
	return &Handler{DB: db, GW: gw}
}

// pgModel 对话框模型选项
type pgModel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Vendor      string `json:"vendor"`
}

// routableJoin 可路由模型的公共 JOIN：启用模型 × 启用渠道（channel_abilities）
const routableJoin = `
	FROM models m
	JOIN channel_abilities ab ON ab.model_name = m.name
	JOIN channels ch ON ch.id = ab.channel_id AND ch.status = 1
	WHERE m.status = 1`

// Models GET /api/playground/models 按角色返回可体验的模型列表
func (h *Handler) Models(c *gin.Context) {
	role := middleware.GetRole(c)
	uid := middleware.GetUID(c)
	orgID := middleware.GetOrgID(c)

	filter := ""
	args := []any{}
	switch role {
	case model.RolePlatformAdmin:
		// 全部可路由模型
	case model.RoleOrgAdmin:
		if orgID == nil {
			httpx.OK(c, gin.H{"models": []pgModel{}})
			return
		}
		filter = ` AND m.name IN (
			SELECT g.model_name FROM user_model_grants g
			JOIN users u ON u.id = g.user_id AND u.org_id = ?)`
		args = append(args, *orgID)
	case model.RoleMember:
		filter = ` AND m.name IN (
			SELECT model_name FROM user_model_grants WHERE user_id = ?)`
		args = append(args, uid)
	default:
		httpx.Fail(c, http.StatusForbidden, "未知角色")
		return
	}

	var models []pgModel
	if err := h.DB.Raw(`SELECT DISTINCT m.id, m.name, m.display_name, m.vendor`+
		routableJoin+filter+` ORDER BY m.name`, args...).Scan(&models).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询模型失败")
		return
	}
	if models == nil {
		models = []pgModel{}
	}
	httpx.OK(c, gin.H{"models": models})
}

// Chat POST /api/playground/chat OpenAI 兼容 body 原样透传给数据面编排
func (h *Handler) Chat(c *gin.Context) {
	role := middleware.GetRole(c)
	uid := middleware.GetUID(c)
	orgID := middleware.GetOrgID(c)

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil || len(body) == 0 || len(body) >= 1<<20 {
		openaiFail(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		openaiFail(c, http.StatusBadRequest, "invalid_request_error", "missing required parameter: model")
		return
	}

	ki := &middleware.KeyInfo{KeyID: 0, Playground: true} // KeyID=0：调用日志中归属「在线体验」，限流走共享桶 key:0
	switch role {
	case model.RolePlatformAdmin:
		// 无客户归属：不预检、不计费（网关侧 Cost 置 0），usage_logs 记 org_id=0
		if !h.modelAllowed(role, 0, nil, req.Model) {
			openaiFail(c, http.StatusForbidden, "model_not_allowed",
				"model is not routable, please check channel abilities")
			return
		}
	case model.RoleOrgAdmin, model.RoleMember:
		if orgID == nil {
			openaiFail(c, http.StatusForbidden, "permission_error", "account has no organization")
			return
		}
		// 账号/客户状态与数据面口径一致：欠费停服（2）与停用（0）都拒绝
		var st struct {
			UID        int64
			UserStatus int
			OrgStatus  int
		}
		if err := h.DB.Raw(`SELECT u.id AS uid, u.status AS user_status, o.status AS org_status
			FROM users u JOIN orgs o ON o.id = u.org_id WHERE u.id = ?`, uid).Scan(&st).Error; err != nil || st.UID == 0 {
			openaiFail(c, http.StatusForbidden, "permission_error", "account not found")
			return
		}
		if st.UserStatus != 1 || st.OrgStatus == 0 {
			openaiFail(c, http.StatusForbidden, "permission_error", "account or organization is disabled")
			return
		}
		if st.OrgStatus == 2 { // 欠费停服：与数据面口径一致
			openaiFail(c, http.StatusForbidden, "insufficient_balance",
				"organization suspended for arrears (quota exhausted), please contact the platform admin to recharge")
			return
		}
		if !h.modelAllowed(role, uid, orgID, req.Model) {
			openaiFail(c, http.StatusForbidden, "model_not_allowed",
				"you are not allowed to use this model in playground")
			return
		}
		ki.UserID, ki.OrgID = uid, *orgID // 真实计费：等同用自己的 key 调用
	default:
		openaiFail(c, http.StatusForbidden, "permission_error", "unknown role")
		return
	}

	// 回填 body 并注入合成身份，复用数据面编排（限流/缓存/选路/转发/结算全链路）
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	middleware.SetKeyInfo(c, ki)
	h.GW.ChatCompletions(c)
}

// modelAllowed 单模型授权校验（与 Models 列表同一口径，防止前端绕过列表直发）
func (h *Handler) modelAllowed(role string, uid int64, orgID *int64, modelName string) bool {
	filter := ""
	var args []any
	switch role {
	case model.RolePlatformAdmin:
	case model.RoleOrgAdmin:
		if orgID == nil {
			return false
		}
		filter = ` AND m.name IN (
			SELECT g.model_name FROM user_model_grants g
			JOIN users u ON u.id = g.user_id AND u.org_id = ?)`
		args = append(args, *orgID)
	case model.RoleMember:
		filter = ` AND m.name IN (
			SELECT model_name FROM user_model_grants WHERE user_id = ?)`
		args = append(args, uid)
	default:
		return false
	}
	var cnt int64
	if err := h.DB.Raw(`SELECT COUNT(DISTINCT m.id)`+routableJoin+filter+` AND m.name = ?`,
		append(args, modelName)...).Scan(&cnt).Error; err != nil {
		return false
	}
	return cnt > 0
}

// openaiFail 数据面错误格式（前端流式客户端按 OpenAI error 结构解析）
func openaiFail(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{"message": msg, "type": errType, "code": errType},
	})
}
