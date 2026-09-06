package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// Register 前端静态资源 + SPA fallback（API 路径不受影响，返回 JSON 404）。
// fsys 为仓库根 //go:embed all:web/dist 的子目录（见 static.go）。
func Register(r *gin.Engine, fsys fs.FS) {
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// API 与数据面路径不落入 SPA
		if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/v1") {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "not found", "type": "not_found"}})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusMethodNotAllowed, gin.H{"error": gin.H{"message": "method not allowed"}})
			return
		}
		clean := strings.TrimPrefix(path.Clean(p), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}
		if data, err := fs.ReadFile(fsys, clean); err == nil {
			ct := mime.TypeByExtension(path.Ext(clean))
			if ct == "" {
				ct = "application/octet-stream"
			}
			if strings.HasPrefix(clean, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000") // vite hash 文件名，可长缓存
			} else {
				c.Header("Cache-Control", "no-cache")
			}
			c.Data(http.StatusOK, ct, data)
			return
		}
		// SPA fallback：所有前端路由都回 index.html
		if data, err := fs.ReadFile(fsys, "index.html"); err == nil {
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", data)
			return
		}
		c.String(http.StatusNotFound, "前端未构建：请先执行 build.sh")
	})
}
