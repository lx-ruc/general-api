package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"token-gateway/internal/scrub"
)

// OK 成功响应
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, data)
}

// Fail 统一错误响应：消息消毒——org/member 平台的 handler 存在 err.Error() 直拼回显，
// 网络/数据库错误的标准形态会带主机地址，客户侧不得见到
func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"message": scrub.Str(msg), "type": "api_error"}})
}

// BindJSON 绑定请求体；失败时已自动响应 400。
// 严格模式：整包 json.Unmarshal 而非 json.Decoder——Decoder 只解出第一个 JSON 值
// 即报成功，尾部垃圾（`{...} junk` / 拼接双对象）被静默忽略，坏请求体被当合法
// 部分解析落库；Unmarshal 对外围空白依旧宽容。校验语义与 ShouldBindJSON 一致
// （Unmarshal 后显式跑 binding 规则）。
func BindJSON(c *gin.Context, obj any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		Fail(c, http.StatusBadRequest, "请求体读取失败: "+err.Error())
		return false
	}
	if err := json.Unmarshal(body, obj); err != nil {
		Fail(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return false
	}
	if err := binding.Validator.ValidateStruct(obj); err != nil {
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
