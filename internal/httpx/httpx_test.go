package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type bindTarget struct {
	Name  string `json:"name" binding:"required"`
	Count int    `json:"count" binding:"min=0"`
}

func postBind(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	BindJSON(c, &bindTarget{})
	return w
}

// #40：JSON 尾部垃圾（拼接/截断的坏请求体）不得被静默部分解析——
// json.Decoder 只解第一个值即报成功，管理面写库接口曾借此把带垃圾的请求当合法创建对象
func TestBindJSONRejectsTrailingGarbage(t *testing.T) {
	if w := postBind(t, `{"name":"a"} trailing`); w.Code != http.StatusBadRequest {
		t.Fatalf("尾部垃圾应 400，got %d body=%s", w.Code, w.Body)
	}
	if w := postBind(t, `{"name":"a"}{"name":"b"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("拼接双对象应 400，got %d", w.Code)
	}
}

// 合法宽容不回归：首尾空白仍应通过（json.Unmarshal 允许外围空白）
func TestBindJSONAcceptsPeripheralWhitespace(t *testing.T) {
	if w := postBind(t, "\n  {\"name\":\"a\"}  \n"); w.Code != http.StatusOK {
		t.Fatalf("首尾空白应 200，got %d body=%s", w.Code, w.Body)
	}
}

// binding 校验语义不回归：required / min 与 ShouldBindJSON 时代一致
func TestBindJSONStillValidates(t *testing.T) {
	if w := postBind(t, `{"count":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 required 字段应 400，got %d", w.Code)
	}
	if w := postBind(t, `{"name":"a","count":-1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("min=0 违反应 400，got %d", w.Code)
	}
	if w := postBind(t, `{"name":"a"}`); w.Code != http.StatusOK {
		t.Fatalf("合法对象应 200，got %d", w.Code)
	}
}
