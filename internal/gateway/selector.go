package gateway

import (
	"math/rand"
	"strings"

	"gorm.io/gorm"

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
}

// URL 上游完整地址 = base_url + path
func (c Candidate) URL() string {
	return strings.TrimRight(c.BaseURL, "/") + c.Path
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

// SelectCandidates 查询模型的路由候选：
// 最高 priority 组内按 weight 加权随机（负载均衡），其余组按优先级顺序 appended 作为降级备用
func SelectCandidates(db *gorm.DB, cipher *crypto.Cipher, modelName string) ([]Candidate, error) {
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
			key, err := cipher.Decrypt(r.UpstreamKeyEnc)
			if err != nil || key == "" {
				continue // 解密失败或未配置上游 key 的渠道直接跳过
			}
			um := ""
			if r.UpstreamModelName != nil {
				um = *r.UpstreamModelName
			}
			cands = append(cands, Candidate{
				ChannelID:     r.ChannelID,
				ChannelName:   r.Name,
				BaseURL:       r.BaseURL,
				Path:          r.Path,
				UpstreamKey:   key,
				UpstreamModel: um,
				Weight:        r.Weight,
				Priority:      r.Priority,
			})
		}
		i = j
	}
	return cands, nil
}

// weightedShuffle 不放回加权随机抽取，得到一个打乱顺序的列表
func weightedShuffle(items []candRow) []candRow {
	remaining := append([]candRow(nil), items...)
	out := make([]candRow, 0, len(remaining))
	for len(remaining) > 0 {
		total := 0
		for _, r := range remaining {
			total += weightOf(r)
		}
		pick, idx := rand.Intn(total), 0
		for k, r := range remaining {
			pick -= weightOf(r)
			if pick < 0 {
				idx = k
				break
			}
		}
		out = append(out, remaining[idx])
		remaining = append(remaining[:idx], remaining[idx+1:]...)
	}
	return out
}

func weightOf(r candRow) int {
	if r.Weight <= 0 {
		return 1
	}
	return r.Weight
}
