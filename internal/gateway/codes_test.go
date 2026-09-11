package gateway

import "testing"

// 状态码表解析：单码 / x x 通配 / 区间 / 混合 / 非法项忽略
func TestParseCodeSetForms(t *testing.T) {
	set := parseCodeSet([]string{"429"})
	if len(set) != 1 || !set[429] {
		t.Fatalf("单码解析错误: %v", set)
	}

	set = parseCodeSet([]string{"5xx"})
	if len(set) != 100 || !set[500] || !set[599] || set[499] {
		t.Fatalf("5xx 通配解析错误: %v", set)
	}

	set = parseCodeSet([]string{"4XX"})
	if len(set) != 100 || !set[400] || !set[499] || set[500] {
		t.Fatalf("大写 4XX 通配解析错误: %v", set)
	}

	set = parseCodeSet([]string{"500-504"})
	if len(set) != 5 || !set[500] || !set[504] || set[505] {
		t.Fatalf("区间解析错误: %v", set)
	}

	set = parseCodeSet([]string{"401", "403", "5xx", "", "bogus", "99", "600", "504-500"})
	if len(set) != 102 || !set[401] || !set[403] || !set[550] {
		t.Fatalf("混合列表（非法项应被忽略）解析错误: %v", set)
	}
}

// 空表恒不命中；未配置（nil）也恒不命中
func TestCodeMatchEmpty(t *testing.T) {
	if codeMatch(nil, 429) || codeMatch(map[int]bool{}, 429) {
		t.Fatal("空表不应命中任何状态码")
	}
}
