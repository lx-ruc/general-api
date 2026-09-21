package gateway

import (
	"math"
	"testing"
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
