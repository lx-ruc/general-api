## 1. 数据层

- [x] 1.1 schema.sql + migrate.go：orgs/users 各加 alert_levels/alert_level/alert_since；model 实体同步
  - orgs.alert_levels TEXT DEFAULT '[80]'（[] = 关）；alert_level/alert_since INTEGER DEFAULT 0；alters 六条幂等加列；Org/User 实体同步

## 2. 状态机与服务层

- [x] 2.1 新 internal/service/alerts.go：bracket 计算、转移规则（升档告警/静默降级/跳档收敛）、CAS 抢占 UPDATE、60s 节流 sync.Map
  - bracket 全整数比较 `used*100 >= limit*t`；CAS `UPDATE ... WHERE alert_level < :new` RowsAffected=1 者发信（多实例安全）；降档静默复位；节流 key "org:{id}"/"user:{id}"
- [x] 2.2 Settle 提交后异步挂检查（go + 恢复 panic）；user 与 org 两级主体
  - gateway/handler.go defer：Settle 成功且 rec.Cost > 0 才 `go service.CheckBudgetAlerts(...)`（缓存命中/被拦截不动水位）；CheckBudgetAlerts 自带 recover
- [x] 2.3 收件扇出：org→全部 org_admin；org 耗尽+平台管理员；user→本人(有邮箱)+org_admin；alert_levels=[]跳过
  - notifier 新增 NotifyPlatformAdmins / NotifyUserAndAdmins，与 NotifyOrgAdmins 共用 sendToAll；不限额（NULL/0 limit）跳过
- [x] 2.4 notifier 复用发信（水位/限额/直达链接；org 耗尽版含"联系平台管理员"）；metrics 两指标
  - 指标：tg_alert_triggers_total{subject}（CAS 胜者）+ tg_alert_throttled_total；直达链接读 server.site_url（新增 config + TG_SITE_URL，空=文字提示）；metrics Handler 抽 writeVec
- [x] 2.5 阈值编辑端点（org/platform）+ 编辑时静默重算 alert_level
  - GET/PUT /api/org/alert-levels（本公司）、PUT /api/platform/orgs/:id/alert-levels；ThresholdToLevels/LevelsToThreshold 收敛在 service；编辑后 RecomputeAlertLevel 静默重算不发信

## 3. 测试与收尾

- [x] 3.1 测试：转移表逐行（跨越单发/维持不重发/79→100 收敛/追加后再告警/拨中间档不重发）、并发 CAS 单发、节流、空数组关闭
  - alerts_test.go 10 个用例全绿：转移表（0→79→80→85→100→拨备回落→再耗尽→拨中间档）、跳档单发（[50,80,100] 一步 100% 只 1 封）、并发 20 检查单胜者、节流（含指标计数）、[] 与 limit=0 关闭、user NULL limit 跳过、静默重算、panic 恢复；发信观察经 sendAlertMailFn 包级注入（不依赖 SMTP 副作用）
- [x] 3.2 前端：org/platform 阈值设置 UI（单阈值输入写入 [T]，0/关）
  - org：对账单页新增「额度预警」卡（GET 初始化 + 保存）；platform：公司详情额度读数卡内嵌阈值编辑（alert_levels 由 apiGetOrg 返回）
- [x] 3.3 `go test ./internal/...` 全绿；`go vet ./...`；`make build` 通过
- [x] 3.4 config.example.yaml/README 增补
  - config.example.yaml server.site_url（ENV: TG_SITE_URL）；README 功能总览 +「额度预警」bullet（两级水位/节流/CAS 唯一/耗尽通知平台/0=关）
