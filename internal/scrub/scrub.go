package scrub

// 错误文本消毒：客户侧一切对外错误信息先过这里，剥掉其中的网络地址，
// 避免把上游渠道拓扑（base_url、厂商域名、解析出的 IP）暴露给客户。
// 三个出口统一接线：
//   - 数据面错误体（gateway openaiError / anthropicError / 上游错误体透传 / SSE 错误事件）
//   - 管理面错误体（httpx.Fail）
//   - usage_logs 落库（error 列对客户管理员可见）
//
// 原始含地址的错误仍完整保留在服务端日志（slog）里供平台管理员排障。

import (
	"regexp"
	"strings"
)

const placeholder = "[filtered]"

// 顺序即优先级：先剥完整 URL（最长），再剥 IPv4[:port]，最后剥裸域名[:port]。
var (
	urlRe  = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>）。，,；;\\]+`)
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{1,5})?\b`)
	// 裸域名：≥1 段标签 + 点 + 纯字母尾段 ≥2（TLD）。
	// 模型名（glm-4.5、qwen2.5-turbo 等点后是数字/含连字符）不会命中。
	hostRe      = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(?::\d{1,5})?\b`)
	localhostRe = regexp.MustCompile(`(?i)\blocalhost(?::\d{1,5})?\b`)
)

// Str 返回消毒后的字符串；无地址特征时原样返回。
func Str(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, "./:") { // 快速通道：绝大多数错误文本不含这三类字符
		return s
	}
	s = urlRe.ReplaceAllString(s, placeholder)
	s = ipv4Re.ReplaceAllString(s, placeholder)
	s = localhostRe.ReplaceAllString(s, placeholder)
	return hostRe.ReplaceAllString(s, placeholder)
}

// Bytes 字节版（上游错误 JSON 体透传前整块消毒）。
func Bytes(b []byte) []byte {
	return []byte(Str(string(b)))
}
