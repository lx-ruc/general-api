package org

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

type orgEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

// newOrgEnv 临时库 + org + org_admin 身份，挂成员管理路由
func newOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	oid := int64(1)
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'admin', 'x', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造管理员失败: %v", err)
	}

	const secret = "test-secret"
	token, err := auth.GenerateToken(secret, time.Hour, 2, "org_admin", &oid)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	og := engine.Group("/api/org", middleware.JWTAuth(secret, db))
	h := NewHandler(db)
	og.POST("/members", h.CreateMember)
	og.PUT("/members/:id", h.UpdateMember)
	og.GET("/cost-centers", h.ListCostCenters)
	og.POST("/cost-centers", h.CreateCostCenter)
	og.PUT("/cost-centers/config", h.UpdateCostCenterConfig)
	og.PUT("/cost-centers/:id", h.UpdateCostCenter)
	og.GET("/reports/cost-centers", h.CostCenterReport)
	og.PUT("/keys/:id/cost-center", h.ReassignKeyCenter)
	return &orgEnv{engine: engine, db: db, token: token}
}

func (e *orgEnv) do(method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// sumUserGrants 某 user 的流水合计
func (e *orgEnv) sumUserGrants(userID int64) int64 {
	var s int64
	_ = e.db.Raw(`SELECT COALESCE(SUM(amount),0) FROM quota_grants WHERE subject_type='user' AND subject_id=?`, userID).Scan(&s).Error
	return s
}

// 建号初始额度入流水；quota_unlimited 切换走差值流水；Σgrants == COALESCE(limit,0) 恒成立
func TestMemberQuotaLedger(t *testing.T) {
	e := newOrgEnv(t)

	// 建号初始额度 1,000,000 → 一条"创建子账号初始额度"流水
	w := e.do(http.MethodPost, "/api/org/members", `{"username":"mem1","password":"pass123","quota_amount":1000000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建号应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var mid int64 // 新成员 id（自增，不假设具体值）
	_ = e.db.Raw(`SELECT id FROM users WHERE username='mem1'`).Scan(&mid).Error
	if mid == 0 {
		t.Fatal("未找到新成员")
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='user' AND subject_id=? AND amount=1000000 AND remark='创建子账号初始额度'`, mid).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("初始额度应入流水，cnt=%d", cnt)
	}
	if s := e.sumUserGrants(mid); s != 1_000_000 {
		t.Fatalf("建号后 Σgrants 应 1,000,000，得 %d", s)
	}

	// 转不限 → 差值流水 −1,000,000，Σ 归零
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":true}`); w.Code != http.StatusOK {
		t.Fatalf("转不限应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if s := e.sumUserGrants(mid); s != 0 {
		t.Fatalf("转不限后 Σgrants 应 0，得 %d", s)
	}
	var nullLimit *int64
	_ = e.db.Raw(`SELECT quota_limit FROM users WHERE id=?`, mid).Scan(&nullLimit).Error
	if nullLimit != nil {
		t.Fatalf("转不限后 limit 应 NULL，得 %v", *nullLimit)
	}

	// 制造消耗后转回限额 → 以消耗为起点
	_ = e.db.Exec(`UPDATE users SET quota_used=300000 WHERE id=?`, mid).Error
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":false}`); w.Code != http.StatusOK {
		t.Fatalf("转限额应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if s := e.sumUserGrants(mid); s != 300_000 {
		t.Fatalf("转限额应以消耗 300,000 为起点，得 Σ=%d", s)
	}
	var limit int64
	_ = e.db.Raw(`SELECT COALESCE(quota_limit,0) FROM users WHERE id=?`, mid).Scan(&limit).Error
	if limit != 300_000 {
		t.Fatalf("limit 应 300,000，得 %d", limit)
	}

	// 同态重复调用：无新流水
	before := e.sumUserGrants(mid)
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":false}`); w.Code != http.StatusOK {
		t.Fatalf("同态调用应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if after := e.sumUserGrants(mid); after != before {
		t.Fatalf("同态调用不应产生流水，%d → %d", before, after)
	}
}
