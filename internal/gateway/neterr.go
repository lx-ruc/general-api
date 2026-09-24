package gateway

import (
	"errors"
	"net"
	"strings"
)

// 上游网络错误分类：数据面终态码（全部超时 → 504）与探测侧中文描述共用。
// http.Client.Do 的错误形态是 *url.Error 包装内层（transport/dial）错误，
// 超时判定用 net.Error.Timeout()（url.Error 会向内层委托），其余按错误文本特征归类。

// netErrKind 网络错误类别
type netErrKind int

const (
	netErrOther   netErrKind = iota // 未归类的网络错误
	netErrTimeout                   // 超时（等待响应头 / 建连 / TLS 握手）
	netErrRefused                   // 连接被拒绝（端口未监听 / 防火墙拦截）
	netErrDNS                       // 域名解析失败
	netErrTLS                       // TLS / 证书校验失败
)

// classifyNetErr 归类上游网络错误。返回类别与管理台可读的中文描述（含处置方向）；
// 原始错误由调用方自行决定是否附带（数据面只进服务端日志，探测侧附在描述后）。
func classifyNetErr(err error) (netErrKind, string) {
	if err == nil {
		return netErrOther, ""
	}
	var ne net.Error
	timeout := errors.As(err, &ne) && ne.Timeout()
	s := err.Error()
	switch {
	case timeout ||
		strings.Contains(s, "timeout awaiting response headers") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "TLS handshake timeout"):
		return netErrTimeout, "上游超时：等待响应或建立连接超时（上游过慢或网络不通），请稍后重试；持续出现请检查渠道健康状态"
	case strings.Contains(s, "connection refused"):
		return netErrRefused, "连接被拒绝：上游端口未监听或被防火墙拦截，请检查渠道 base_url 与路径"
	case strings.Contains(s, "no such host") || strings.Contains(s, "lookup ") && strings.Contains(s, ":"):
		return netErrDNS, "域名解析失败：base_url 主机名不存在或本机 DNS 异常，请检查渠道地址拼写"
	case strings.Contains(s, "tls:") || strings.Contains(s, "x509") || strings.Contains(s, "certificate"):
		return netErrTLS, "TLS/证书错误：上游证书校验失败，请检查渠道地址协议（http/https）与证书有效性"
	}
	return netErrOther, "网络错误：无法连接上游（连接被重置 / 网络不可达等），请检查渠道地址与网络"
}

// DescribeNetErr 管理台侧网络错误文案：中文分类描述 + 原始错误（排障需要细节；
// 面向系统管理员，base_url 本就是其配置项，无需消毒）。渠道探测与「从上游获取
// 模型」共用
func DescribeNetErr(err error) string {
	_, desc := classifyNetErr(err)
	return desc + "。原始错误：" + truncateStr(err.Error(), 300)
}

// kindString 类别的日志短名（结构化日志可读）
func kindString(k netErrKind) string {
	switch k {
	case netErrTimeout:
		return "timeout"
	case netErrRefused:
		return "refused"
	case netErrDNS:
		return "dns"
	case netErrTLS:
		return "tls"
	}
	return "other"
}
