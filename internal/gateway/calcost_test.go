package gateway

import (
	"math"
	"testing"

	"token-gateway/internal/model"
)

// CalcCost 的输入 tokens 来自上游响应体（不可信），单价来自管理台定价；
// 乘加与 ceil 必须在任何 int64 输入下都不回绕：回绕为负 = 免费放行，
// 回绕为任意值 = 错误计费，+999_999 回绕 = 负 cost 污染报表与勾稽链。
func TestCalcCost(t *testing.T) {
	cases := []struct {
		name           string
		pt, ct, ip, op int64
		want           int64
	}{
		{"正常计费 ceil 向上", 100, 50, 2_000_000, 2_000_000, 300},
		{"除尽不加 ceil", 500, 0, 2_000_000, 0, 1000},
		{"1 token 也要 ceil", 1, 0, 2_000_000, 0, 2},
		{"零 tokens", 0, 0, 2_000_000, 2_000_000, 0},
		{"零价模型", 100, 100, 0, 0, 0},
		{"负 tokens 按 0 计", -100, -50, 2_000_000, 2_000_000, 0},
		{"负价按 0 计", 100, 50, -2_000_000, -2_000_000, 0},
		// 溢出攻击面（裸乘法行为）：
		//   5e12 × 2e6 = 1e19 > MaxInt64 → 回绕为负 → 旧代码免费；新代码钳制 tokens 后正常计费
		{"tokens 超限钳制后计费", 5_000_000_000_000, 0, 2_000_000, 0, maxUsageTokens / 1_000_000 * 2_000_000},
		// 2^62 × 2e6 ≡ 0 (mod 2^64)：裸乘法恰好得 0（免费）
		{"2^62 对齐回绕防护", 1 << 62, 0, 2_000_000, 0, maxUsageTokens / 1_000_000 * 2_000_000},
		{"MaxInt64 tokens 钳制", math.MaxInt64, math.MaxInt64, 2_000_000, 2_000_000, maxUsageTokens / 1_000_000 * 2_000_000 * 2},
		// 单笔成本天花板：超出 sane 上限时封顶，防下游 quota_used 累加回绕
		{"MaxInt64 单价封顶", 1_000_000, 0, math.MaxInt64, 0, maxUsagePoints},
		{"组合封顶", maxUsageTokens, maxUsageTokens, math.MaxInt64, math.MaxInt64, maxUsagePoints},
		// 乘积 9.2e18 恰在 MaxInt64 边缘：128 位乘除下不回绕、结果精确
		{"乘积贴 MaxInt64 精确", 9_223_372, 0, 1_000_000_000_000, 0, 9_223_372_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CalcCost(c.pt, c.ct, c.ip, c.op)
			if got != c.want {
				t.Errorf("CalcCost(%d, %d, %d, %d) = %d, want %d", c.pt, c.ct, c.ip, c.op, got, c.want)
			}
			if got < 0 {
				t.Errorf("成本绝不可为负: %d", got)
			}
		})
	}
}

// CalcCostCached 缓存分段计费：命中段按缓存价、未命中段按输入价；
// 缓存价 0 = 同输入价（存量模型计费零变化）；命中数超 prompt 按全命中计。
func TestCalcCostCached(t *testing.T) {
	cases := []struct {
		name                     string
		pt, hit, ct, ip, chp, op int64
		want                     int64
	}{
		// 100 输入（60 命中 40 未命中）×¥2/M + 50 输出 ×¥3/M = (40×2 + 60×0.5 + 50×3) = 260 点
		{"命中更便宜分段计", 100, 60, 50, 2_000_000, 500_000, 3_000_000, 260},
		// 缓存价 0 = 同输入价：与 CalcCost 完全一致
		{"缓存价 0 回退输入价", 100, 60, 50, 2_000_000, 0, 3_000_000, CalcCost(100, 50, 2_000_000, 3_000_000)},
		{"未配置缓存价全命中也同价", 1_000_000, 1_000_000, 0, 2_000_000, 0, 0, 2_000_000},
		// 全命中：只按缓存价收
		{"全部命中", 1_000_000, 1_000_000, 0, 2_000_000, 500_000, 0, 500_000},
		{"全未命中", 1_000_000, 0, 0, 2_000_000, 500_000, 0, 2_000_000},
		// 上游脏数据：命中 > prompt → 按 prompt 封顶（不放大账单）
		{"命中超 prompt 钳制", 100, 999_999, 0, 2_000_000, 0, 0, 200},
		{"负命中按 0", 100, -50, 0, 2_000_000, 500_000, 0, 200},
		{"负缓存价按 0=同输入价", 100, 100, 0, 2_000_000, -1, 0, 200},
		// ceil 向上：分段和为小数点时向上取整
		{"ceil 向上", 1, 1, 0, 2_000_000, 1_000_000, 0, 1},
		// 溢出面：分段乘加同样 128 位安全
		{"分段组合封顶", maxUsageTokens, maxUsageTokens, maxUsageTokens, math.MaxInt64, math.MaxInt64, math.MaxInt64, maxUsagePoints},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CalcCostCached(c.pt, c.hit, c.ct, c.ip, c.chp, c.op)
			if got != c.want {
				t.Errorf("CalcCostCached(%d, %d, %d, %d, %d, %d) = %d, want %d",
					c.pt, c.hit, c.ct, c.ip, c.chp, c.op, got, c.want)
			}
			if got < 0 {
				t.Errorf("成本绝不可为负: %d", got)
			}
		})
	}
}

// Usage 两种缓存方言归一：DeepSeek prompt_cache_hit_tokens / OpenAI prompt_tokens_details.cached_tokens，
// 同时出现取较大者；CachedTokens 只是原始读数，钳到 prompt_tokens 由计费侧负责
func TestUsageCachedTokens(t *testing.T) {
	deepseek := Usage{PromptTokens: 100, PromptCacheHitTokens: 60}
	if got := deepseek.CachedTokens(); got != 60 {
		t.Errorf("DeepSeek 方言应取 60, got %d", got)
	}
	openai := Usage{PromptTokens: 100, PromptTokensDetails: &struct {
		CachedTokens int64 `json:"cached_tokens"`
	}{CachedTokens: 70}}
	if got := openai.CachedTokens(); got != 70 {
		t.Errorf("OpenAI 方言应取 70, got %d", got)
	}
	both := Usage{PromptTokens: 100, PromptCacheHitTokens: 30, PromptTokensDetails: openai.PromptTokensDetails}
	if got := both.CachedTokens(); got != 70 {
		t.Errorf("双方言并存应取较大 70, got %d", got)
	}
	none := Usage{PromptTokens: 100}
	if got := none.CachedTokens(); got != 0 {
		t.Errorf("无缓存字段应取 0, got %d", got)
	}
}

// applyUsage 缓存计费快照：cached_tokens / 缓存价快照落日志，Cost 与 VendorCost 分段计；
// 未配置缓存价时计费与旧口径逐点一致（升级零影响）
func TestApplyUsageCachedPricing(t *testing.T) {
	m := model.Model{
		Name: "m", InputPrice: 2_000_000, OutputPrice: 3_000_000, InputCacheHitPrice: 500_000,
		CostInputPrice: 1_000_000, CostOutputPrice: 1_500_000, CostInputCacheHitPrice: 100_000,
	}
	rec := &model.UsageLog{}
	applyUsage(rec, &Usage{PromptTokens: 100, PromptTokensDetails: &struct {
		CachedTokens int64 `json:"cached_tokens"`
	}{CachedTokens: 60}, CompletionTokens: 50}, m)

	if rec.CachedTokens != 60 {
		t.Errorf("cached_tokens = %d, want 60", rec.CachedTokens)
	}
	if rec.InputCacheHitPrice != 500_000 || rec.CostInputCacheHitPrice != 100_000 {
		t.Errorf("缓存价快照不符: %+v", rec)
	}
	// 售卖：(40×2 + 60×0.5 + 50×3) = 260；成本：(40×1 + 60×0.1 + 50×1.5) = 121
	if rec.Cost != 260 {
		t.Errorf("Cost = %d, want 260", rec.Cost)
	}
	if rec.VendorCost != 121 {
		t.Errorf("VendorCost = %d, want 121", rec.VendorCost)
	}

	// 未配置缓存价（0）：与 CalcCost 旧口径完全一致
	old := model.Model{Name: "m2", InputPrice: 2_000_000, OutputPrice: 3_000_000}
	rec2 := &model.UsageLog{}
	applyUsage(rec2, &Usage{PromptTokens: 100, PromptCacheHitTokens: 60, CompletionTokens: 50}, old)
	if rec2.Cost != CalcCost(100, 50, 2_000_000, 3_000_000) {
		t.Errorf("缓存价 0 应与旧口径一致: %d", rec2.Cost)
	}

	// 命中 > prompt 钳制
	rec3 := &model.UsageLog{}
	applyUsage(rec3, &Usage{PromptTokens: 100, PromptCacheHitTokens: 500, CompletionTokens: 0}, old)
	if rec3.CachedTokens != 100 {
		t.Errorf("命中超 prompt 应钳到 100, got %d", rec3.CachedTokens)
	}
	if rec3.Cost != CalcCost(100, 0, 2_000_000, 0) {
		t.Errorf("钳制后应按全量输入价计: %d", rec3.Cost)
	}
}
