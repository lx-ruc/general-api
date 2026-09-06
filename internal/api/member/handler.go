package member

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/httpx"
	"token-gateway/internal/auth"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

type Handler struct {
	DB *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{DB: db}
}

func uid(c *gin.Context) int64 { return middleware.GetUID(c) }

// ownKey 校验密钥属于当前员工
func (h *Handler) ownKey(c *gin.Context, id int64) (*model.APIKey, bool) {
	var k model.APIKey
	if err := h.DB.Where("id = ? AND user_id = ?", id, uid(c)).First(&k).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "密钥不存在")
		return nil, false
	}
	return &k, true
}

// ---------------- 密钥管理 ----------------

// ListKeys GET /api/member/keys
func (h *Handler) ListKeys(c *gin.Context) {
	var keys []model.APIKey
	_ = h.DB.Where("user_id = ?", uid(c)).Order("id DESC").Find(&keys).Error
	if keys == nil {
		keys = []model.APIKey{}
	}
	httpx.OK(c, keys)
}

// CreateKey POST /api/member/keys：明文完整 key 仅此一次返回
func (h *Handler) CreateKey(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var u model.User
	if err := h.DB.Where("id = ?", uid(c)).First(&u).Error; err != nil || u.OrgID == nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return
	}
	plain, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	k := model.APIKey{
		OrgID: *u.OrgID, UserID: u.ID, Name: req.Name,
		KeyPrefix: prefix, KeyHash: hash, Status: 1,
	}
	if err := h.DB.Create(&k).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存密钥失败")
		return
	}
	httpx.OK(c, gin.H{
		"id": k.ID, "name": k.Name, "key": plain,
		"key_prefix": prefix, "created_at": k.CreatedAt,
		"message": "请立即保存完整密钥，关闭后将无法再次查看",
	})
}

// DeleteKey DELETE /api/member/keys/:id
func (h *Handler) DeleteKey(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if _, ok := h.ownKey(c, id); !ok {
		return
	}
	if err := h.DB.Where("id = ?", id).Delete(&model.APIKey{}).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// ---------------- 模型与额度 ----------------

// ListModels GET /api/member/models：可用模型 + 单价 + 我的余额
func (h *Handler) ListModels(c *gin.Context) {
	type modelRow struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Vendor      string `json:"vendor"`
		InputPrice  int64  `json:"input_price"`
		OutputPrice int64  `json:"output_price"`
	}
	var models []modelRow
	_ = h.DB.Raw(`
		SELECT m.name, m.display_name, m.vendor, m.input_price, m.output_price
		FROM user_model_grants g JOIN models m ON m.name = g.model_name
		WHERE g.user_id = ? AND m.status = 1 ORDER BY m.name`, uid(c)).Scan(&models).Error
	if models == nil {
		models = []modelRow{}
	}
	var u model.User
	_ = h.DB.Where("id = ?", uid(c)).First(&u).Error
	httpx.OK(c, gin.H{
		"models":           models,
		"quota_limit":      u.QuotaLimit,
		"quota_used":       u.QuotaUsed,
		"points_per_yuan":  service.PointsPerYuan(h.DB),
	})
}

// StatsOverview GET /api/member/stats/overview
func (h *Handler) StatsOverview(c *gin.Context) {
	id := uid(c)
	ov, err := service.StatsOverview(h.DB, service.Scope{UserID: &id})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "统计查询失败")
		return
	}
	httpx.OK(c, ov)
}

// ListUsage GET /api/member/usage
func (h *Handler) ListUsage(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "l.user_id = ?", []any{uid(c)}
	if v := c.Query("model"); v != "" {
		cond += " AND l.model_name = ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "status", 0); v > 0 {
		cond += " AND l.status = ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "start", 0); v > 0 {
		cond += " AND l.created_at >= ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "end", 0); v > 0 {
		cond += " AND l.created_at < ?"
		args = append(args, v)
	}
	var total int64
	_ = h.DB.Raw("SELECT COUNT(*) FROM usage_logs l WHERE "+cond, args...).Scan(&total).Error
	var rows []model.UsageLog
	_ = h.DB.Raw("SELECT l.* FROM usage_logs l WHERE "+cond+" ORDER BY l.id DESC LIMIT ? OFFSET ?",
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []model.UsageLog{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// ---------------- 额度申请 ----------------

// ListRequests GET /api/member/requests
func (h *Handler) ListRequests(c *gin.Context) {
	var rows []model.QuotaRequest
	_ = h.DB.Where("user_id = ?", uid(c)).Order("id DESC").Limit(50).Find(&rows).Error
	if rows == nil {
		rows = []model.QuotaRequest{}
	}
	httpx.OK(c, rows)
}

// CreateRequest POST /api/member/requests
func (h *Handler) CreateRequest(c *gin.Context) {
	var req struct {
		Amount int64  `json:"amount" binding:"required,gt=0"`
		Reason string `json:"reason"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var u model.User
	if err := h.DB.Where("id = ?", uid(c)).First(&u).Error; err != nil || u.OrgID == nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return
	}
	qr := model.QuotaRequest{
		OrgID: *u.OrgID, UserID: u.ID, Amount: req.Amount,
		Reason: req.Reason, Status: "pending",
	}
	if err := h.DB.Create(&qr).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "提交申请失败")
		return
	}
	httpx.OK(c, qr)
}
