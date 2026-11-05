package platform

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/gateway"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// displayNameOf 展示名（空=用标识名）
func displayNameOf(v model.Vendor) string {
	if v.DisplayName != "" {
		return v.DisplayName
	}
	return v.Name
}

// ---------------- 厂商管理（一行一厂商：Key 池 + 端点 + 模型概览） ----------------

// vendorEndpoint 厂商行内的端点（渠道）概览
type vendorEndpoint struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	BaseURL     string            `json:"base_url"`
	Path        string            `json:"path"`
	Weight      int               `json:"weight"`
	Priority    int               `json:"priority"`
	Status      int               `json:"status"`
	LastTestAt  *int64            `json:"last_test_at"`
	LastTestOk  int               `json:"last_test_ok"`
	AutoStopAt  int64             `json:"auto_disabled_at"`
	Models      []vendorEPModel   `json:"models"`
}

type vendorEPModel struct {
	ModelName         string  `json:"model_name"`
	UpstreamModelName *string `json:"upstream_model_name"`
}

// ListVendors GET /api/platform/vendors：一行一厂商聚合（Key 角标/端点/模型/客户开通数）
func (h *Handler) ListVendors(c *gin.Context) {
	var vendors []model.Vendor
	_ = h.DB.Order("id").Find(&vendors).Error
	if vendors == nil {
		vendors = []model.Vendor{}
	}
	// Key 池计数（总/启用/冷却）
	keyCounts := h.vendorKeyCounts()
	cooling := h.vendorKeyCoolingCounts()
	// 端点与其模型能力
	type epRow struct {
		ID         int64
		VendorID   int64
		Name       string
		BaseURL    string
		Path       string
		Weight     int
		Priority   int
		Status     int
		LastTestAt *int64
		LastTestOk int
		AutoStopAt int64 `gorm:"column:auto_disabled_at"`
	}
	var eps []epRow
	_ = h.DB.Raw(`SELECT id, vendor_id, name, base_url, path, weight, priority, status,
		last_test_at, last_test_ok, auto_disabled_at FROM channels ORDER BY priority DESC, id`).Scan(&eps).Error
	type abRow struct {
		ChannelID         int64
		ModelName         string
		UpstreamModelName *string
	}
	var abs []abRow
	_ = h.DB.Raw("SELECT channel_id, model_name, upstream_model_name FROM channel_abilities ORDER BY model_name").Scan(&abs).Error
	epsByVendor := map[int64][]vendorEndpoint{}
	modelsByEp := map[int64][]vendorEPModel{}
	for _, a := range abs {
		modelsByEp[a.ChannelID] = append(modelsByEp[a.ChannelID], vendorEPModel{a.ModelName, a.UpstreamModelName})
	}
	for _, e := range eps {
		ms := modelsByEp[e.ID]
		if ms == nil {
			ms = []vendorEPModel{}
		}
		epsByVendor[e.VendorID] = append(epsByVendor[e.VendorID], vendorEndpoint{
			ID: e.ID, Name: e.Name, BaseURL: e.BaseURL, Path: e.Path,
			Weight: e.Weight, Priority: e.Priority, Status: e.Status,
			LastTestAt: e.LastTestAt, LastTestOk: e.LastTestOk, AutoStopAt: e.AutoStopAt, Models: ms,
		})
	}
	// 各厂商的组内模型名集合（经端点能力聚合）
	type vmRow struct {
		VendorID  int64
		ModelName string
	}
	var vms []vmRow
	_ = h.DB.Raw(`SELECT DISTINCT c.vendor_id, a.model_name FROM channel_abilities a
		JOIN channels c ON c.id = a.channel_id`).Scan(&vms).Error
	modelsByVendor := map[int64][]string{}
	for _, r := range vms {
		modelsByVendor[r.VendorID] = append(modelsByVendor[r.VendorID], r.ModelName)
	}
	// 客户开通数
	var grantRows []struct {
		VendorID int64
		Cnt      int64
	}
	_ = h.DB.Raw("SELECT vendor_id, COUNT(*) AS cnt FROM org_vendor_grants GROUP BY vendor_id").Scan(&grantRows).Error
	orgCnt := map[int64]int64{}
	for _, r := range grantRows {
		orgCnt[r.VendorID] = r.Cnt
	}

	list := make([]gin.H, 0, len(vendors))
	for _, v := range vendors {
		kc := keyCounts[v.ID]
		ms := modelsByVendor[v.ID]
		if ms == nil {
			ms = []string{}
		}
		lastTest := maxLastTest(epsByVendor[v.ID])
		list = append(list, gin.H{
			"id": v.ID, "name": v.Name, "display_name": displayNameOf(v),
			"status": v.Status, "remark": v.Remark,
			"has_key": kc.Active > 0, "key_count": kc.Total, "key_active_count": kc.Active,
			"key_cooling_count": cooling[v.ID],
			"model_count":       len(ms), "models": ms,
			"endpoint_count":    len(epsByVendor[v.ID]), "endpoints": epsByVendor[v.ID],
			"org_count":         orgCnt[v.ID],
			"last_test_at":      lastTest.at, "last_test_ok": lastTest.ok,
			"created_at": v.CreatedAt,
		})
	}
	httpx.OK(c, list)
}

type lastTestRes struct {
	at *int64
	ok int
}

func maxLastTest(eps []vendorEndpoint) lastTestRes {
	var best *int64
	ok := 0
	for _, e := range eps {
		if e.LastTestAt != nil && (best == nil || *e.LastTestAt > *best) {
			best = e.LastTestAt
			ok = e.LastTestOk
		}
	}
	return lastTestRes{best, ok}
}

type vendorReq struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name"`
	Remark      string `json:"remark"`
	Status      *int   `json:"status"`
}

// CreateVendor POST /api/platform/vendors
func (h *Handler) CreateVendor(c *gin.Context) {
	var req vendorReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpx.Fail(c, http.StatusBadRequest, "厂商标识不能为空")
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Vendor{}).Where("name = ?", name).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "厂商标识已存在")
		return
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	v := model.Vendor{Name: name, DisplayName: req.DisplayName, Status: status, Remark: req.Remark}
	if err := h.DB.Create(&v).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "创建厂商失败")
		return
	}
	httpx.OK(c, gin.H{"id": v.ID, "message": "厂商已创建"})
}

// GetVendor GET /api/platform/vendors/:id
func (h *Handler) GetVendor(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var v model.Vendor
	if err := h.DB.Where("id = ?", id).First(&v).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "厂商不存在")
		return
	}
	httpx.OK(c, v)
}

// UpdateVendor PUT /api/platform/vendors/:id（改名需同步 channels/models 冗余文本时无冗余——
// vendor 文本列 C5 删除，实体 id 关联不受改名影响）
func (h *Handler) UpdateVendor(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		DisplayName *string `json:"display_name"`
		Remark      *string `json:"remark"`
		Status      *int    `json:"status"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			httpx.Fail(c, http.StatusBadRequest, "厂商标识不能为空")
			return
		}
		var cnt int64
		_ = h.DB.Model(&model.Vendor{}).Where("name = ? AND id != ?", name, id).Count(&cnt).Error
		if cnt > 0 {
			httpx.Fail(c, http.StatusBadRequest, "厂商标识已存在")
			return
		}
		updates["name"] = name
	}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
			return
		}
		updates["status"] = *req.Status
	}
	res := h.DB.Model(&model.Vendor{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "厂商不存在")
		return
	}
	// 厂商启停改变全部端点的可路由性 → 对齐模型启用态
	service.SyncModelsAndNotify(h.DB, fmt.Sprintf("厂商 #%d %s", id, map[int]string{0: "停用", 1: "启用"}[*req.Status]))
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteVendor DELETE /api/platform/vendors/:id：前置校验无端点/模型归属/Key/授权/定价
func (h *Handler) DeleteVendor(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	checks := []struct {
		cond string
		msg  string
	}{
		{"SELECT COUNT(*) FROM channels WHERE vendor_id = ?", "仍有端点挂在该厂商下"},
		{"SELECT COUNT(*) FROM channel_abilities a JOIN channels c ON c.id = a.channel_id WHERE c.vendor_id = ?", "仍有模型能力挂在该厂商的端点上"},
		{"SELECT COUNT(*) FROM models WHERE vendor_id = ?", "仍有模型归属该厂商"},
		{"SELECT COUNT(*) FROM vendor_keys WHERE vendor_id = ?", "厂商 Key 池非空（请先移除 Key）"},
		{"SELECT COUNT(*) FROM org_vendor_grants WHERE vendor_id = ?", "仍有客户开通该厂商"},
		{"SELECT COUNT(*) FROM org_vendor_prices WHERE vendor_id = ?", "仍有客户厂商价配置"},
	}
	for _, ck := range checks {
		var n int64
		_ = h.DB.Raw(ck.cond, id).Scan(&n).Error
		if n > 0 {
			httpx.Fail(c, http.StatusBadRequest, "无法删除："+ck.msg)
			return
		}
	}
	if res := h.DB.Where("id = ?", id).Delete(&model.Vendor{}); res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "厂商不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// AddVendorKeys POST /api/platform/vendors/:id/keys：向厂商 Key 池追加（单把或批量）
func (h *Handler) AddVendorKeys(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if !h.vendorExists(vid) {
		httpx.Fail(c, http.StatusNotFound, "厂商不存在")
		return
	}
	var req struct {
		Keys   []string `json:"keys" binding:"required,min=1"`
		Weight int      `json:"weight"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	seen := map[string]bool{}
	rows := make([]model.VendorKey, 0, len(req.Keys))
	now := time.Now().Unix()
	for _, k := range req.Keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		enc, err := h.Cipher.Encrypt(k)
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
			return
		}
		rows = append(rows, model.VendorKey{
			VendorID: vid, KeyEnc: enc, Weight: weight, Status: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(rows) == 0 {
		httpx.Fail(c, http.StatusBadRequest, "没有可新增的 Key")
		return
	}
	if err := h.DB.Create(&rows).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	service.SyncModelsAndNotify(h.DB, fmt.Sprintf("厂商 #%d 追加 %d 把 Key", vid, len(rows)))
	httpx.OK(c, gin.H{"added": len(rows)})
}

func (h *Handler) vendorExists(vid int64) bool {
	var cnt int64
	_ = h.DB.Raw("SELECT COUNT(*) FROM vendors WHERE id = ?", vid).Scan(&cnt).Error
	return cnt > 0
}

// ListVendorKeys GET /api/platform/vendors/:id/keys（打码，不回明文/密文）
func (h *Handler) ListVendorKeys(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if !h.vendorExists(vid) {
		httpx.Fail(c, http.StatusNotFound, "厂商不存在")
		return
	}
	var keys []model.VendorKey
	if err := h.DB.Where("vendor_id = ?", vid).Order("id").Find(&keys).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	list := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		plain, derr := h.Cipher.Decrypt(k.KeyEnc)
		if derr != nil {
			plain = ""
		}
		scope := fmt.Sprintf("ck:%d:%d", vid, k.ID)
		list = append(list, gin.H{
			"id": k.ID, "key_masked": maskKey(plain), "weight": k.Weight,
			"status": k.Status, "remark": k.Remark,
			"cooling": h.Coord.IsCooling(scope), "quota_cooling": h.Coord.IsQuotaCooling(scope),
			"created_at": k.CreatedAt, "updated_at": k.UpdatedAt,
		})
	}
	httpx.OK(c, list)
}

// GetVendorKeyFull GET /api/platform/vendors/:id/keys/:kid/full —— 复制完整厂商 Key
func (h *Handler) GetVendorKeyFull(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	service.CopyKeyPayload(c, h.DB, h.Cipher, "厂商 Key", middleware.GetUID(c),
		"SELECT id, key_enc AS enc FROM vendor_keys WHERE id = ? AND vendor_id = ?", kid, vid)
}

// UpdateVendorKeyStatus PUT /api/platform/vendors/:id/keys/:kid/status
func (h *Handler) UpdateVendorKeyStatus(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	var req struct {
		Status *int `json:"status" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if *req.Status != 0 && *req.Status != 1 {
		httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
		return
	}
	res := h.DB.Exec(`UPDATE vendor_keys SET status = ?,
		remark = CASE WHEN ? = 1 THEN '' ELSE remark END, updated_at = ?
		WHERE id = ? AND vendor_id = ?`, *req.Status, *req.Status, time.Now().Unix(), kid, vid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "Key 不存在")
		return
	}
	service.SyncModelsAndNotify(h.DB, fmt.Sprintf("厂商 #%d 的 Key #%d %s", vid, kid, map[int]string{0: "禁用", 1: "启用"}[*req.Status]))
	httpx.OK(c, gin.H{"message": "已更新"})
}

// ClearVendorKeyCooldown POST /api/platform/vendors/:id/keys/:kid/cooldown/clear
func (h *Handler) ClearVendorKeyCooldown(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	var cnt int64
	if err := h.DB.Raw("SELECT COUNT(*) FROM vendor_keys WHERE id = ? AND vendor_id = ?", kid, vid).Scan(&cnt).Error; err != nil || cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "Key 不存在")
		return
	}
	h.Coord.ClearCooldown(fmt.Sprintf("ck:%d:%d", vid, kid))
	httpx.OK(c, gin.H{"message": "已清除冷却"})
}

// DeleteVendorKey DELETE /api/platform/vendors/:id/keys/:kid；池删空清该厂商端点能力
func (h *Handler) DeleteVendorKey(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	res := h.DB.Exec("DELETE FROM vendor_keys WHERE id = ? AND vendor_id = ?", kid, vid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "Key 不存在")
		return
	}
	var left int64
	_ = h.DB.Raw("SELECT COUNT(*) FROM vendor_keys WHERE vendor_id = ?", vid).Scan(&left).Error
	if left == 0 {
		_ = h.DB.Exec(`DELETE FROM channel_abilities WHERE channel_id IN
			(SELECT id FROM channels WHERE vendor_id = ?)`, vid).Error
	}
	service.SyncModelsAndNotify(h.DB, fmt.Sprintf("厂商 #%d 删除 Key #%d", vid, kid))
	httpx.OK(c, gin.H{"message": "已删除"})
}

// TestVendor POST /api/platform/vendors/:id/test：探测该厂商优先级最高的启用端点
func (h *Handler) TestVendor(c *gin.Context) {
	vid, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var ch model.Channel
	if err := h.DB.Raw(`SELECT * FROM channels WHERE vendor_id = ? AND status = 1
		ORDER BY priority DESC, id LIMIT 1`, vid).Scan(&ch).Error; err != nil || ch.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, "该厂商没有启用中的端点")
		return
	}
	res := gateway.ProbeChannel(h.DB, h.Cipher, h.Client, h.Coord, 0, ch.ID)
	if res.Err != "" && res.Status == 0 && !res.OK {
		if strings.Contains(res.Err, "未配置") {
			httpx.Fail(c, http.StatusBadRequest, res.Err)
			return
		}
	}
	_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = ?, updated_at = ? WHERE id = ?",
		time.Now().Unix(), b2i(res.OK), time.Now().Unix(), ch.ID).Error
	httpx.OK(c, gin.H{"ok": res.OK, "status": res.Status, "latency_ms": res.LatencyMs,
		"error": res.Err, "key": res.KeyDesc, "quota_exhausted": res.Quota, "channel_id": ch.ID})
}

