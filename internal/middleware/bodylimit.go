package middleware

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// drainSlack 超限排水封顶在 limit 之外的额外余量：合理超限（多传了几 MB）的
// 客户端也能收到可读的 413；恶意超大上传止于封顶即断，排水量有界。
const drainSlack = 8 << 20

// DrainRequestBody 有界丢弃剩余请求体。超限拒绝时若一点不读 body 就回 413 并
// 关连接，内核会因收端残留数据发 RST，"先写完 body 再读响应" 的客户端
// （Python urllib / curl 等非全双工实现）只见到 connection reset，读不到错误响应。
// 最多再读 limit+drainSlack 字节。
func DrainRequestBody(r io.Reader, limit int64) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, limit+drainSlack))
}

// MaxBody 管理面请求体上限（字节）：管理 API 均为小 JSON，超限直接 413，
// 防止恶意超大 body 被 ShouldBindJSON 无界读入内存。
// ContentLength 已知（>0）时预检快速拒绝（先有界排水再回 413，理由见 DrainRequestBody）；
// chunked / 未知长度用 MaxBytesReader 兜底，读到超限时后续 Bind 报错返回 400，
// 连接随之关闭，不会占住内存。limit<=0 视为不启用。
func MaxBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit <= 0 {
			c.Next()
			return
		}
		if c.Request.ContentLength > limit {
			DrainRequestBody(c.Request.Body, limit)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": gin.H{"message": "请求体过大", "type": "api_error"},
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
