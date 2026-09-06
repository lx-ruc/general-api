package httpx

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// OK 成功响应
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, data)
}

// Fail 统一错误响应
func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"message": msg, "type": "api_error"}})
}

// BindJSON 绑定请求体；失败时已自动响应 400
func BindJSON(c *gin.Context, obj any) bool {
	if err := c.ShouldBindJSON(obj); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return false
	}
	return true
}

// PathID 解析路径参数 id
func PathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		Fail(c, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// PageParams 分页参数（page 从 1 起，page_size 默认 20 上限 100）
func PageParams(c *gin.Context) (page, size, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size, (page - 1) * size
}

// PageResult 分页列表响应
func PageResult(c *gin.Context, list any, total int64, page, size int) {
	c.JSON(http.StatusOK, gin.H{
		"list": list, "total": total, "page": page, "page_size": size,
	})
}

// QueryInt64 query 参数转 int64（缺省 def）
func QueryInt64(c *gin.Context, key string, def int64) int64 {
	v := c.Query(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
