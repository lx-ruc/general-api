package agenthelper

// Agent 一键接入助手托管（对齐智谱 coding-helper 的能力）：
//   GET /agent-helper     → sh 引导器（curl 拉取 .mjs 后交 node 执行）
//   GET /agent-helper.mjs → 零依赖 Node 脚本（install/uninstall/status/交互向导）
// 网关地址按请求 Origin 注入脚本（反代场景取 X-Forwarded-Proto/Host），
// 两文件不含任何密钥，可公开访问；每次请求实时替换占位符，禁用缓存。

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const basePlaceholder = "__HUIMU_BASE__"

//go:embed bootstrap.sh
var bootstrapSH string

//go:embed helper.mjs
var helperJS string

// requestBase 还原客户端实际访问的网关根地址（http://host[:port]，不带路径）
func requestBase(c *gin.Context) string {
	scheme := "http"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func serveWithBase(c *gin.Context, body, contentType string) {
	base := requestBase(c)
	if base == "" {
		c.String(http.StatusBadRequest, "cannot determine gateway base url")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, []byte(strings.ReplaceAll(body, basePlaceholder, base)))
}

// Register 挂载 /agent-helper 与 /agent-helper.mjs（需在 SPA fallback 之前注册）
func Register(r *gin.Engine) {
	r.GET("/agent-helper", func(c *gin.Context) {
		serveWithBase(c, bootstrapSH, "text/x-shellscript; charset=utf-8")
	})
	r.GET("/agent-helper.mjs", func(c *gin.Context) {
		serveWithBase(c, helperJS, "text/javascript; charset=utf-8")
	})
}
