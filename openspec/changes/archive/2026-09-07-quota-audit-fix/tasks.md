## 1. 设值路径改造

- [x] 1.1 platform UpdateOrg 设值分支：同事务读旧值→SET→INSERT 差值 grant（remark=设值调整）；CreateOrg 初始额度入流水（remark=初始额度）
  - 复核结论：UpdateOrg 本就不触碰 quota_limit（无设值分支）；CreateOrg/HandleRecharge 初始与追加已有流水，本次将 HandleRecharge 的流水移入加额度同事务（原在事务外且忽略错误）
- [x] 1.2 org 编辑员工额度 SET 分支与置 NULL 分支同改造；成员创建初始额度入流水
  - 新增 `service.SetUserQuotaUnlimited`（转不限记 −旧值 / 转限额以当前消耗为起点，同事务差值入流水，PG 行锁）；UpdateMember 改调它；CreateMember 初始额度已有流水（测试锁定）；HandleQuotaRequest 流水移入同事务
- [x] 1.3 全仓 grep `quota_limit` 复核无遗漏写路径（收尾门禁）
  - 遗漏写路径清单收敛为：CreateOrg/CreateMember（建号初始，带流水）、HandleRecharge/HandleQuotaRequest（追加，带流水）、AddOrgQuota/AddUserQuota（追加，带流水）、SetUserQuotaUnlimited（设值，带流水）——全部入账

## 2. 测试与收尾

- [x] 2.1 测试：设值/置空/建号各产生差值流水；Σgrants=limit 不变量；追加路径行为不变
  - `service/quota_test.go`（不变量逐断言）、`api/org/handler_test.go`（建号+切换全链路）、`api/platform/handler_test.go`（建公司初始）
- [x] 2.2 `go test ./internal/...` 全绿；`go vet ./...`
- [x] 2.3 CLAUDE.md 计费不变量一节补一句"设值路径亦入流水"
