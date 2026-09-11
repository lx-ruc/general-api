package gateway

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// 状态码处置表：把配置里的字符串列表解析成 O(1) 查找的集合，
// 支持三种写法（可混用，逗号分隔的 yaml 列表或环境变量）：
//   - "429"     单个状态码
//   - "5xx"     通配整段（500-599）
//   - "500-504" 闭区间
//
// 分类优先级（handler 转发编排里固定）：换 Key > 禁 Key > 换渠道；
// 未命中任何表的响应按现状处理：2xx 成功、其余 4xx 透传不重试。
func parseCodeSet(items []string) map[int]bool {
	set := make(map[int]bool)
	for _, raw := range items {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		codes, err := parseCodeToken(tok)
		if err != nil {
			slog.Warn("忽略无法解析的状态码配置", "token", tok, "err", err)
			continue
		}
		for _, c := range codes {
			set[c] = true
		}
	}
	return set
}

// parseCodeToken 解析单个 token：429 / 5xx / 500-504
func parseCodeToken(tok string) ([]int, error) {
	if len(tok) == 3 && (tok[1] == 'x' && tok[2] == 'x' || tok[1] == 'X' && tok[2] == 'X') {
		base := int(tok[0]-'0') * 100
		if base < 100 || base > 500 {
			return nil, fmt.Errorf("通配段越界: %q", tok)
		}
		out := make([]int, 0, 100)
		for c := base; c < base+100; c++ {
			out = append(out, c)
		}
		return out, nil
	}
	if lo, hi, found := strings.Cut(tok, "-"); found {
		l, err1 := strconv.Atoi(lo)
		h, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || l < 100 || h > 599 || l > h {
			return nil, fmt.Errorf("区间非法: %q", tok)
		}
		out := make([]int, 0, h-l+1)
		for c := l; c <= h; c++ {
			out = append(out, c)
		}
		return out, nil
	}
	c, err := strconv.Atoi(tok)
	if err != nil || c < 100 || c > 599 {
		return nil, fmt.Errorf("状态码非法: %q", tok)
	}
	return []int{c}, nil
}

// codeMatch 命中判定（空表恒 false）
func codeMatch(set map[int]bool, code int) bool {
	return set != nil && set[code]
}
