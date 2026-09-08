package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/api/member"
	"token-gateway/internal/api/org"
	"token-gateway/internal/api/platform"
	"token-gateway/internal/api/playground"
	"token-gateway/internal/config"
	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
	"token-gateway/internal/gateway"
	"token-gateway/internal/metrics"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
	"token-gateway/internal/webui"
)

// SetupRouter 装配全部路由：/healthz、/api（管理台）、/v1（数据面）、前端静态
func SetupRouter(cfg *config.Config, db *gorm.DB, cipher *crypto.Cipher, webDist fs.FS) *gin.Engine {
	if os.Getenv("TG_DEBUG") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID())

	if len(cfg.Server.CORSOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins:     cfg.Server.CORSOrigins,
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Authorization", "Content-Type"},
			AllowCredentials: false,
		}))
	}

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus 指标；security.metrics_token 非空时需 ?token= 或 Bearer 校验
	m := metrics.New()
	r.GET("/metrics", func(c *gin.Context) {
		if cfg.Security.MetricsToken != "" {
			tok := c.Query("token")
			if tok == "" {
				tok = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			}
			if tok != cfg.Security.MetricsToken {
				c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "invalid metrics token"}})
				return
			}
		}
		m.Handler()(c.Writer, c.Request)
	})

	// ---- 协调器（Key 冷却/渠道闸门/缓存）：配了 Redis 用全局实现，否则进程内存 ----
	coordinator := newCoordinator(cfg, m)

	// ---- 数据面 /v1（API key 鉴权 + per-key 限流）----
	keyLimiter := middleware.NewRateLimiter(cfg.Gateway.PerKeyRPM, 10)
	gw := gateway.NewHandler(db, cipher, cfg, keyLimiter, m, coordinator)
	v1 := r.Group("/v1", middleware.APIKeyAuth(db))
	{
		v1.POST("/chat/completions", gw.ChatCompletions)
		v1.GET("/models", gw.ListModels)
	}

	// ---- 管理台 /api ----
	service.SetMailer(&cfg.Smtp)
	service.SetSiteURL(cfg.Server.SiteURL)
	service.SetBillingTimezone(cfg.Billing.Timezone)
	// 月末余额快照（次月 1 日 00:05 账期时区；启动自愈补跑；settings CAS 多实例唯一）
	go service.RunBalanceSnapshotter(db, cfg.Billing.Timezone)
	verif := service.NewVerification(db, &cfg.Smtp)
	authH := &AuthHandler{DB: db, Secret: cfg.Security.JWTSecret, TTL: cfg.Security.JWTTTL.Duration, Verif: verif}
	loginLimiter := middleware.NewRateLimiter(5, 5)
	codeLimiter := middleware.NewRateLimiter(3, 3)
	apiGrp := r.Group("/api")
	apiGrp.POST("/auth/login", loginLimiter.Middleware(
		func(c *gin.Context) string { return c.ClientIP() },
		"rate_limit_error", "尝试过于频繁，请稍后再试",
	), authH.Login)
	apiGrp.POST("/auth/send-code", codeLimiter.Middleware(
		func(c *gin.Context) string { return c.ClientIP() },
		"rate_limit_error", "发送太频繁，请稍后再试",
	), authH.SendCode)
	apiGrp.POST("/auth/register", authH.Register)

	authed := apiGrp.Group("", middleware.JWTAuth(cfg.Security.JWTSecret, db), middleware.Audit(db))
	{
		authed.GET("/me", authH.Me)
		authed.PUT("/me/password", authH.ChangePassword)
	}

	// 在线体验（三角色通用）：注入合成身份复用数据面编排，JWT 保证身份
	pg := playground.NewHandler(db, gw)
	authed.GET("/playground/models", pg.Models)
	authed.POST("/playground/chat", pg.Chat)

	// 系统管理员
	ph := platform.NewHandler(db, cipher, gw.Client)
	plat := authed.Group("/platform", middleware.RequireRole(model.RolePlatformAdmin))
	{
		plat.GET("/orgs", ph.ListOrgs)
		plat.POST("/orgs", ph.CreateOrg)
		plat.GET("/orgs/:id", ph.GetOrg)
		plat.PUT("/orgs/:id", ph.UpdateOrg)
		plat.DELETE("/orgs/:id", ph.DeleteOrg)
		plat.POST("/orgs/:id/quota", ph.AddOrgQuota)
		plat.PUT("/orgs/:id/alert-levels", ph.UpdateOrgAlertLevels)
		plat.POST("/orgs/:id/reset-admin-password", ph.ResetOrgAdminPassword)
		plat.GET("/orgs/:id/users", ph.ListOrgUsers)
		plat.GET("/orgs/:id/stats", ph.OrgStats)
		plat.GET("/orgs/:id/statement", ph.OrgStatement)
		plat.GET("/orgs/:id/statement/csv", ph.OrgStatementCSV)
		plat.POST("/billing/snapshots", ph.RunSnapshot)
		plat.GET("/vendor-bills", ph.ListVendorBills)
		plat.PUT("/vendor-bills", ph.UpsertVendorBill)
		plat.DELETE("/vendor-bills/:id", ph.DeleteVendorBill)

		plat.GET("/channels", ph.ListChannels)
		plat.POST("/channels", ph.CreateChannel)
		plat.GET("/channels/:id", ph.GetChannel)
		plat.PUT("/channels/:id", ph.UpdateChannel)
		plat.PUT("/channels/:id/status", ph.UpdateChannelStatus)
		plat.DELETE("/channels/:id", ph.DeleteChannel)
		plat.POST("/channels/:id/test", ph.TestChannel)
		plat.GET("/channels/:id/keys", ph.ListChannelKeys)
		plat.PUT("/channels/:id/keys/:kid/status", ph.UpdateChannelKeyStatus)

		plat.GET("/models", ph.ListModels)
		plat.POST("/models", ph.CreateModel)
		plat.PUT("/models/:id", ph.UpdateModel)
		plat.DELETE("/models/:id", ph.DeleteModel)

		plat.GET("/stats/overview", ph.StatsOverview)
		plat.GET("/usage", ph.ListUsage)
		plat.GET("/reports/cost-centers", ph.CostCenterCrossReport)
		plat.GET("/recharges", ph.ListRecharges)
		plat.PUT("/recharges/:id", ph.HandleRecharge)
		plat.GET("/audit", ph.ListAudit)
		plat.GET("/bank-info", ph.GetBankInfo)
		plat.PUT("/bank-info", ph.UpdateBankInfo)
	}

	// 客户管理员（org 隔离：handler 内强制 WHERE org_id）
	oh := org.NewHandler(db)
	og := authed.Group("/org", middleware.RequireRole(model.RoleOrgAdmin))
	{
		og.GET("/members", oh.ListMembers)
		og.POST("/members", oh.CreateMember)
		og.GET("/members/:id", oh.GetMember)
		og.PUT("/members/:id", oh.UpdateMember)
		og.DELETE("/members/:id", oh.DeleteMember)
		og.POST("/members/:id/reset-password", oh.ResetMemberPassword)
		og.POST("/members/:id/quota", oh.AddMemberQuota)
		og.GET("/members/:id/models", oh.GetMemberModels)
		og.PUT("/members/:id/models", oh.SetMemberModels)

		og.GET("/stats/overview", oh.StatsOverview)
		og.GET("/usage", oh.ListUsage)
		og.GET("/keys", oh.ListKeys)
		og.PUT("/keys/:id/status", oh.UpdateKeyStatus)
		og.PUT("/keys/:id/cost-center", oh.ReassignKeyCenter)

		og.GET("/cost-centers", oh.ListCostCenters)
		og.POST("/cost-centers", oh.CreateCostCenter)
		og.PUT("/cost-centers/config", oh.UpdateCostCenterConfig)
		og.PUT("/cost-centers/:id", oh.UpdateCostCenter)
		og.GET("/reports/cost-centers", oh.CostCenterReport)

		og.GET("/requests", oh.ListRequests)
		og.PUT("/requests/:id", oh.HandleRequest)
		og.GET("/recharges", oh.ListRecharges)
		og.POST("/recharges", oh.CreateRecharge)
		og.GET("/bank-info", oh.GetBankInfo)
		og.GET("/billing", oh.Billing)
		og.GET("/billing/statement", oh.BillingStatement)
		og.GET("/billing/statement/csv", oh.BillingStatementCSV)
		og.GET("/alert-levels", oh.GetAlertLevels)
		og.PUT("/alert-levels", oh.UpdateAlertLevels)
	}

	// 子账号
	mh := member.NewHandler(db)
	mg := authed.Group("/member", middleware.RequireRole(model.RoleMember))
	{
		mg.GET("/keys", mh.ListKeys)
		mg.POST("/keys", mh.CreateKey)
		mg.DELETE("/keys/:id", mh.DeleteKey)
		mg.PUT("/keys/:id/cost-center", mh.AssignKeyCenter)
		mg.GET("/cost-centers", mh.ListCostCenters)
		mg.GET("/models", mh.ListModels)
		mg.GET("/stats/overview", mh.StatsOverview)
		mg.GET("/usage", mh.ListUsage)
		mg.GET("/requests", mh.ListRequests)
		mg.POST("/requests", mh.CreateRequest)
	}

	// 前端静态（embed，SPA fallback）
	webui.Register(r, webDist)
	return r
}

// newCoordinator 按配置构建协调器：Redis（多实例全局）优先，失败或未配置回退内存实现
func newCoordinator(cfg *config.Config, m *metrics.Metrics) coord.Coordinator {
	if cfg.Redis.Addr != "" {
		rc, err := coord.NewRedis(coord.RedisConfig{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			OnError:  m.CoordRedisErrors.Inc,
		})
		if err != nil {
			slog.Error("Redis 协调器连接失败，回退内存模式（多实例下闸门/冷却为 per-node）",
				"addr", cfg.Redis.Addr, "err", err)
		} else {
			slog.Info("Redis 协调器已启用（闸门/冷却/缓存全局共享）", "addr", cfg.Redis.Addr)
			return rc
		}
	}
	if cfg.Database.Driver == "postgres" {
		slog.Warn("postgres 部署未配置 redis：闸门/冷却/缓存为 per-node，多实例下实际并发≈max×节点数")
	}
	return coord.NewMem(cfg.Gateway.CacheMaxItems)
}
