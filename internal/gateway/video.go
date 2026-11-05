package gateway

// 视频生成任务中转（火山 Ark 异步任务式）：
//
//   POST /v1/videos/generations/tasks          创建任务（relay 内核透传，Ark 原生协议）
//   GET  /v1/videos/generations/tasks/:task_id 查询任务（本文件独立 handler）
//
// 创建走 relay（限流/授权/预检/渠道选择/Key池/映射/熔断全部复用），上游只回 {"id"}
// 无 OpenAI 形状 usage → 自动落 NoUsage=1 不计量；计费发生在首次查询到 succeeded 时，
// 按 usage.completion_tokens 结算（官方口径：视频不统计输入 token，此值即计费量）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// videoTaskPath 火山方舟视频生成任务端点（创建与查询共用前缀）
const videoTaskPath = "/contents/generations/tasks"

// Ark 任务状态四态
const (
	videoStatusSucceeded = "succeeded"
	videoStatusFailed    = "failed"
)

// videoTaskIDRe 上游任务 id 合法字符集（拼进出站 URL 前校验，防注入）
var videoTaskIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// VideoCreateTask POST /v1/videos/generations/tasks：Ark 原生透传创建任务
func (h *Handler) VideoCreateTask(c *gin.Context) {
	h.relay(c, relaySpec{
		fixedPath:    videoTaskPath,
		require:      []string{"content"},
		modelType:    "video",
		videoCapture: true,
	})
}

// captureVideoTask 创建任务 2xx 后捕获上游任务 id 落库：查询路由（task→渠道）与
// 结算（归属/价格快照）都依赖这行登记。失败仅日志——任务已被上游受理，不能因登记
// 失败影响已发出的 2xx；漏登记的任务查询不到、也结不了账（损失封顶单任务），客户端
// 重试创建即可恢复
func (h *Handler) captureVideoTask(rec *model.UsageLog, data []byte, m model.Model) {
	var tr struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &tr) != nil || tr.ID == "" {
		return
	}
	if rec.ChannelID == nil || *rec.ChannelID == 0 {
		return
	}
	vt := &model.VideoTask{
		TaskID:          tr.ID,
		OrgID:           rec.OrgID,
		UserID:          rec.UserID,
		APIKeyID:        rec.APIKeyID,
		CostCenterID:    rec.CostCenterID,
		ChannelID:       *rec.ChannelID,
		ModelName:       rec.ModelName,
		OutputPrice:     m.OutputPrice,     // 已含客户覆盖价（relay 在查找后改写过 m）
		CostOutputPrice: m.CostOutputPrice, // 成本价保持模型级不变
		CreateRequestID: rec.RequestID,
	}
	if err := h.DB.Create(vt).Error; err != nil {
		slog.Error("视频任务登记失败（该任务将无法查询/结算）",
			"task_id", tr.ID, "model", rec.ModelName, "err", err)
	}
}

// errVideoAlreadyBilled 结算抢占败者（双网关并发 / 重复查询），非错误
var errVideoAlreadyBilled = errors.New("video task already billed")

// settleVideoTask 视频任务结算（幂等）：同一事务内 UPDATE … WHERE billed=0 抢占，
// 抢到者执行 settleTx（双记账 + usage_logs）。结算无条件执行（欠费也扣）——上游已
// 生成并收费，与「断连也计费」不变量一致。PG 行锁 + READ COMMITTED 下败者重读为
// 0 行；SQLite 由 _txlock=immediate 单写者串行化
func (h *Handler) settleVideoTask(task *model.VideoTask, status string, tokens, latencyMs int64, clientIP string) error {
	tokens = clampTokens(tokens)
	rec := &model.UsageLog{
		RequestID:        task.TaskID, // 对账锚点：结算行可直接关联上游任务
		OrgID:            task.OrgID,
		UserID:           task.UserID,
		APIKeyID:         task.APIKeyID,
		CostCenterID:     task.CostCenterID,
		ChannelID:        &task.ChannelID,
		ModelName:        task.ModelName,
		CompletionTokens: tokens,
		OutputPrice:      task.OutputPrice, // 创建时快照，改价不影响已建任务的账单
		CostOutputPrice:  task.CostOutputPrice,
		Status:           http.StatusOK,
		LatencyMs:        latencyMs,
		ClientIP:         clientIP,
	}
	if task.OrgID == 0 {
		// 在线体验合成身份无额度语义（防御分支：当前 Playground 只有 chat 入口）
		rec.Cost, rec.VendorCost = 0, 0
	} else {
		rec.Cost = CalcCostCached(0, 0, tokens, 0, 0, task.OutputPrice)
		rec.VendorCost = CalcCostCached(0, 0, tokens, 0, 0, task.CostOutputPrice)
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE video_tasks SET billed = 1, status = ?, completion_tokens = ?, updated_at = ?
			WHERE task_id = ? AND billed = 0`, status, tokens, time.Now().Unix(), task.TaskID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errVideoAlreadyBilled
		}
		return service.SettleTx(tx, rec)
	})
	if errors.Is(err, errVideoAlreadyBilled) {
		return nil // 对方已结算，视为成功
	}
	if err == nil && rec.Cost > 0 {
		go service.CheckBudgetAlerts(h.DB, h.Metrics, rec.OrgID, rec.UserID)
	}
	return err
}

// upstreamVideoTask Ark 任务查询响应中网关关心的字段
type upstreamVideoTask struct {
	Status string  `json:"status"` // queued/running/succeeded/failed
	Usage  *Usage  `json:"usage"`  // succeeded 时带 completion_tokens（计费量）
	Error  *struct { // 任务失败时的错误说明（透传给客户端，不参与结算）
		Message string `json:"message"`
	} `json:"error"`
}

// fetchUpstreamTask 只读查询上游任务：GET {base}/contents/generations/tasks/{id}。
// 返回（HTTP 状态码, 原始响应体[已做模型名映射改写], 解析出的任务状态）。
// 渠道被禁用/熔断不拦截——钱已在创建时花掉，查询只读 + 结算
func (h *Handler) fetchUpstreamTask(ctx context.Context, task *model.VideoTask) (int, []byte, *upstreamVideoTask, error) {
	var ch struct {
		BaseURL string
		Path    string
	}
	if err := h.DB.Raw("SELECT base_url, path FROM channels WHERE id = ?", task.ChannelID).
		Scan(&ch).Error; err != nil {
		return 0, nil, nil, err
	}
	if ch.BaseURL == "" || ch.Path == "" {
		return 0, nil, nil, fmt.Errorf("channel %d base_url/path not configured", task.ChannelID)
	}
	key, ok := h.videoChannelKey(task.ChannelID)
	if !ok {
		return 0, nil, nil, fmt.Errorf("channel %d has no usable upstream key", task.ChannelID)
	}
	// 模型映射改写回对外名（映射对客户不可见，与 chat 响应同口径）
	var ab struct {
		UpstreamModelName *string
	}
	_ = h.DB.Raw("SELECT upstream_model_name FROM channel_abilities WHERE channel_id = ? AND model_name = ?",
		task.ChannelID, task.ModelName).Scan(&ab).Error

	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet,
		endpointURL(ch.BaseURL, ch.Path, videoTaskPath)+"/"+task.TaskID, nil)
	if rerr != nil {
		return 0, nil, nil, rerr
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "token-gateway")
	resp, derr := h.Client.Do(req)
	if derr != nil {
		return 0, nil, nil, derr
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if rerr != nil {
		return 0, nil, nil, rerr
	}
	if ab.UpstreamModelName != nil && *ab.UpstreamModelName != "" && *ab.UpstreamModelName != task.ModelName {
		body = rewriteModel(body, [2]string{*ab.UpstreamModelName, task.ModelName})
	}
	var up upstreamVideoTask
	if json.Unmarshal(body, &up) == nil && up.Status != "" {
		return resp.StatusCode, body, &up, nil
	}
	return resp.StatusCode, body, nil, nil // 非 JSON / 无状态字段：原样透传，不动账本
}

// videoChannelKey 取渠道所属厂商一把可用上游 Key（只读查询用）：厂商池内启用 Key
// 依次解密取首把成功者（不看冷却——查询不改调度状态）
func (h *Handler) videoChannelKey(chID int64) (string, bool) {
	var krs []struct{ KeyEnc string }
	if err := h.DB.Raw(`SELECT vk.key_enc FROM vendor_keys vk
		JOIN channels c ON c.vendor_id = vk.vendor_id
		WHERE c.id = ? AND vk.status = 1 ORDER BY vk.id`, chID).Scan(&krs).Error; err == nil {
		for _, kr := range krs {
			if k, derr := h.Cipher.Decrypt(kr.KeyEnc); derr == nil && k != "" {
				return k, true
			}
		}
	}
	return "", false
}

// VideoTaskQuery GET /v1/videos/generations/tasks/:task_id
// 查询任务并原样透传上游响应；首次观察到 succeeded 且带 usage 时结算一次。
// 任务归属隔离：不存在与他客户任务一律 404（不可区分，防跨客户探测任务）
func (h *Handler) VideoTaskQuery(c *gin.Context) {
	start := time.Now()
	ki := middleware.GetKeyInfo(c)
	taskID := c.Param("task_id")
	if !videoTaskIDRe.MatchString(taskID) {
		openaiError(c, http.StatusBadRequest, "invalid_request_error", "invalid task id")
		return
	}
	// 与 relay 同口径的 per-key 限流（轮询查询也占额度）
	if h.Limiter != nil && !h.Limiter.Allow(fmt.Sprintf("key:%d", ki.KeyID)) {
		openaiError(c, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded, please retry later")
		return
	}
	var task model.VideoTask
	if err := h.DB.Where("task_id = ?", taskID).First(&task).Error; err != nil ||
		(ki.OrgID != task.OrgID && !ki.Playground) {
		openaiError(c, http.StatusNotFound, "invalid_request_error", "video task not found")
		return
	}
	status, body, up, ferr := h.fetchUpstreamTask(c.Request.Context(), &task)
	metricStatus := status
	if ferr != nil {
		metricStatus = http.StatusBadGateway
		slog.Warn("视频任务查询上游失败", "task_id", taskID,
			"channel_id", task.ChannelID, "err", ferr.Error())
		openaiError(c, http.StatusBadGateway, "upstream_error",
			"failed to query video task upstream, please retry later")
	} else {
		// 上游响应原样透传（含任务过期 404 等非 2xx，由客户端自行解释）
		c.Data(status, "application/json", body)
	}
	if h.Metrics != nil {
		h.Metrics.Requests.With(strconv.Itoa(metricStatus)).Inc()
		h.Metrics.Latency.Observe(time.Since(start).Seconds())
	}
	if ferr != nil {
		return
	}
	latency := time.Since(start).Milliseconds()
	if err := h.observeVideoTask(&task, up, latency, c.ClientIP()); err != nil {
		// 结算失败不影响已透传的响应；补偿扫描会兜底重试
		slog.Error("视频任务结算失败（补偿扫描将重试）", "task_id", taskID, "err", err)
		if h.Metrics != nil {
			h.Metrics.SettleErrors.Inc()
		}
	}
}

// observeVideoTask 查询/补偿扫描共用的结算分支：按观测到的任务状态推进
//   - succeeded + usage → 结算一次（幂等）
//   - succeeded 无 usage → 仅记状态（无法计费，扫描到 24h 放弃线自然止步）
//   - failed → 记终态（无账单，扫描不再重试）
//   - queued/running → 轻量更新观测状态（不落 usage_logs，防轮询刷日志）
func (h *Handler) observeVideoTask(task *model.VideoTask, up *upstreamVideoTask, latencyMs int64, clientIP string) error {
	if up == nil {
		return nil
	}
	now := time.Now().Unix()
	switch up.Status {
	case videoStatusSucceeded:
		if up.Usage == nil {
			_ = h.DB.Exec(`UPDATE video_tasks SET status = ?, updated_at = ? WHERE task_id = ?`,
				videoStatusSucceeded, now, task.TaskID).Error
			return nil
		}
		return h.settleVideoTask(task, videoStatusSucceeded, up.Usage.CompletionTokens, latencyMs, clientIP)
	case videoStatusFailed:
		_ = h.DB.Exec(`UPDATE video_tasks SET status = ?, updated_at = ?
			WHERE task_id = ? AND billed = 0`, videoStatusFailed, now, task.TaskID).Error
		return nil
	default:
		_ = h.DB.Exec(`UPDATE video_tasks SET status = ?, updated_at = ? WHERE task_id = ?`,
			up.Status, now, task.TaskID).Error
		return nil
	}
}

// VideoTaskSweepLoop 后台补偿扫描：客户建完任务从不查询（脚本崩溃/只取任务 id）时的
// 漏单兜底——定时查上游并把 succeeded 未结算的任务结掉。多实例各自运行（重复只读
// GET 无害），结算幂等由 billed 抢占保证。interval<=0 不启动
func VideoTaskSweepLoop(interval time.Duration, h *Handler) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		VideoTaskSweepOnce(h)
	}
}

// videoSweepGiveUpSec 放弃线：视频任务分钟级完成，创建后 24h 仍未结算/未终态视为
// 上游丢弃或不可恢复，留痕不再扫（billed 恒 0，可按 video_tasks.created_at 排查）
const videoSweepGiveUpSec int64 = 24 * 3600

// VideoTaskSweepOnce 扫一轮未结算任务（每轮上限 100 条，防积压时集中打爆上游）
func VideoTaskSweepOnce(h *Handler) {
	cutoff := time.Now().Unix() - videoSweepGiveUpSec
	var tasks []model.VideoTask
	if err := h.DB.Raw(`SELECT * FROM video_tasks
		WHERE billed = 0 AND status <> ? AND created_at >= ?
		ORDER BY created_at LIMIT 100`, videoStatusFailed, cutoff).Scan(&tasks).Error; err != nil {
		slog.Warn("视频任务补偿扫描查询失败", "err", err)
		return
	}
	for i := range tasks {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _, up, ferr := h.fetchUpstreamTask(ctx, &tasks[i])
		cancel()
		if ferr != nil {
			slog.Warn("视频任务补偿扫描查询上游失败", "task_id", tasks[i].TaskID, "err", ferr.Error())
			continue
		}
		if err := h.observeVideoTask(&tasks[i], up, 0, ""); err != nil {
			slog.Error("视频任务补偿扫描结算失败", "task_id", tasks[i].TaskID, "err", err)
		}
	}
}
