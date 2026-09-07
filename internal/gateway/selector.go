package gateway

import (
	"fmt"
	"math/rand"
	"strings"

	"gorm.io/gorm"

	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
)

// Candidate 一个可用的上游渠道候选（已解密上游 key、已确定上游模型名）
type Candidate struct {
	ChannelID     int64
	ChannelName   string
	BaseURL       string
	Path          string
	UpstreamKey   string
	UpstreamModel string // 空 = 与对外模型名同名透传
	Weight        int
	Priority      int
	KeyID         int64 // 0 = 单 Key 兼容模式（channels.upstream_key_enc）
}

// URL 上游完整地址 = base_url + path
func (c Candidate) URL() string {
	return strings.TrimRight(c.BaseURL, "/") + c.Path
}

// KeyScope 冷却 scope：单 Key 模式用 legacy 标记，避免不同渠道互相误伤
func (c Candidate) KeyScope() string {
	if c.KeyID == 0 {
		return fmt.Sprintf("ck:%d:legacy", c.ChannelID)
	}
	return fmt.Sprintf("ck:%d:%d", c.ChannelID, c.KeyID)
}

// SlotScope 渠道并发闸门 scope
func (c Candidate) SlotScope() string {
	return fmt.Sprintf("ch:%d", c.ChannelID)
}

type keyRow struct {
	ChannelID int64
	ID        int64
	KeyEnc    string
	Weight    int
}

type candRow struct {
	ChannelID         int64
	Name              string
	BaseURL           string
	Path              string
	UpstreamKeyEnc    string
	UpstreamModelName *string
	Weight            int
	Priority          int
}

// SelectCandidates 查询模型的路由候选（渠道 × Key）：
// 最高 priority 组内按 weight 加权随机（负载均衡），其余组按优先级顺序 appended 作为降级备用。
// 渠道内：Key 池（channel_keys）过滤禁用与冷却后按权重洗牌；无池回退单 Key 兼容模式。
func SelectCandidates(db *gorm.DB, cipher *crypto.Cipher, modelName string, cd coord.Coordinator) ([]Candidate, error) {
	var rows []candRow
	err := db.Raw(`
		SELECT c.id AS channel_id, c.name, c.base_url, c.path, c.upstream_key_enc,
		       a.upstream_model_name, c.weight, c.priority
		FROM channels c
		JOIN channel_abilities a ON a.channel_id = c.id
		WHERE a.model_name = ? AND c.status = 1
		ORDER BY c.priority DESC`, modelName).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	// base_url/path 校验
	valid := rows[:0]
	for _, r := range rows {
		if r.BaseURL == "" || r.Path == "" {
			continue
		}
		valid = append(valid, r)
	}
	rows = valid
	if len(rows) == 0 {
		return nil, nil
	}

	// 各渠道的 Key 池（一次查全，避免 N+1）
	pools := map[int64][]keyRow{}
	{
		var krs []keyRow
		chIDs := make([]int64, 0, len(rows))
		for _, r := range rows {
			chIDs = append(chIDs, r.ChannelID)
		}
		if err := db.Raw("SELECT channel_id, id, key_enc, weight FROM channel_keys WHERE status = 1 AND channel_id IN ? ORDER BY id", chIDs).Scan(&krs).Error; err != nil {
			return nil, err
		}
		for _, kr := range krs {
			pools[kr.ChannelID] = append(pools[kr.ChannelID], kr)
		}
	}

	cands := make([]Candidate, 0, len(rows))
	i := 0
	for i < len(rows) {
		// 同 priority 分组
		j := i
		for j < len(rows) && rows[j].Priority == rows[i].Priority {
			j++
		}
		group := rows[i:j]
		if len(cands) == 0 {
			group = weightedShuffle(group) // 首组（最高优先级）做负载均衡
		}
		for _, r := range group {
			cands = append(cands, channelCandidates(r, cipher, pools[r.ChannelID], cd)...)
		}
		i = j
	}
	return cands, nil
}

// channelCandidates 单渠道展开为多个候选：优先 Key 池（过滤禁用/冷却 + 权重洗牌），无池回退单 Key
func channelCandidates(r candRow, cipher *crypto.Cipher, pool []keyRow, cd coord.Coordinator) []Candidate {
	upstreamModel := ""
	if r.UpstreamModelName != nil {
		upstreamModel = *r.UpstreamModelName
	}
	mk := func(keyID int64, key, um string, w int) Candidate {
		return Candidate{
			ChannelID: r.ChannelID, ChannelName: r.Name, BaseURL: r.BaseURL, Path: r.Path,
			UpstreamKey: key, UpstreamModel: um, Weight: w, Priority: r.Priority, KeyID: keyID,
		}
	}

	if len(pool) > 0 {
		usable := pool[:0:0]
		for _, kr := range pool {
			if cd != nil && cd.IsCooling(fmt.Sprintf("ck:%d:%d", r.ChannelID, kr.ID)) {
				continue // 冷却中的 Key 跳过
			}
			key, err := cipher.Decrypt(kr.KeyEnc)
			if err != nil || key == "" {
				continue
			}
			usable = append(usable, keyRow{ID: kr.ID, KeyEnc: key, Weight: kr.Weight})
		}
		if len(usable) == 0 {
			return nil // 池内全部冷却/解密失败 → 该渠道本轮不可用
		}
		out := make([]Candidate, 0, len(usable))
		for _, kr := range weightedShuffle(usable) {
			out = append(out, mk(kr.ID, kr.KeyEnc, upstreamModel, kr.Weight))
		}
		return out
	}

	// 单 Key 兼容模式
	if cd != nil && cd.IsCooling(fmt.Sprintf("ck:%d:legacy", r.ChannelID)) {
		return nil
	}
	key, err := cipher.Decrypt(r.UpstreamKeyEnc)
	if err != nil || key == "" {
		return nil
	}
	return []Candidate{mk(0, key, upstreamModel, r.Weight)}
}

// weightedShuffle 不放回加权随机抽取，得到一个打乱顺序的列表（权重<=0 按 1 计）
func weightedShuffle[T any](items []T) []T {
	if len(items) <= 1 {
		return items
	}
	remaining := make([]int, len(items))
	for k := range remaining {
		remaining[k] = k
	}
	out := make([]T, 0, len(items))
	for len(remaining) > 0 {
		total := 0
		for _, idx := range remaining {
			total += intWeight(any(items[idx]))
		}
		pick, pos := rand.Intn(total), 0
		for k, idx := range remaining {
			pick -= intWeight(any(items[idx]))
			if pick < 0 {
				pos = k
				break
			}
		}
		out = append(out, items[remaining[pos]])
		remaining = append(remaining[:pos], remaining[pos+1:]...)
	}
	return out
}

// intWeight 提取权重（candRow / keyRow 都有 Weight int 字段；缺省 1）
func intWeight(v any) int {
	switch t := v.(type) {
	case candRow:
		if t.Weight <= 0 {
			return 1
		}
		return t.Weight
	case keyRow:
		if t.Weight <= 0 {
			return 1
		}
		return t.Weight
	}
	return 1
}
