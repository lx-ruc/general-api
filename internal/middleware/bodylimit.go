package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBody 管理面请求体上限（字节）：管理 API 均为小 JSON，超限直接 413，
// 防止恶意超大 body 被 ShouldBindJSON 无界读入内存。
// ContentLength 已知（>0）时预检快速拒绝；chunked / 未知长度用 MaxBytesReader 兜底，
// 读到超限时后续 Bind 报错返回 400，连接随之关闭，不会占住内存。
// limit<=0 视为不启用。
func MaxBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit <= 0 {
			c.Next()
			return
		}
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": gin.H{"message": "请求体过大", "type": "api_error"},
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
