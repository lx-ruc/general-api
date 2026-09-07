## 1. 数据层

- [x] 1.1 schema.sql + migrate.go alters：orgs +contact_name/contact_phone/monthly_quota/monthly_cost/monthly_period；users +monthly_* 三列（存量库幂等加列，默认 0/'' 不动既有行为）
- [x] 1.2 model：Org/User 补对应字段（json 直出，org 成员列表 u.* 自动带出）

## 2. 小差距收口

- [x] 2.1 联系人字段：CreateOrg/UpdateOrg 接收 contact_name/contact_phone；建司表单补两输入、列表加联系人列、详情页标题区展示
- [x] 2.2 欠费停服：Settle 同事务 `status=1 且额度耗尽 → 2`；AddOrgQuota 后 `status=2 且有余量 → 1`（手动停用 0 不被误恢复）；apiauth 对 status=2 返回 403 insufficient_balance（欠费文案）；前端状态标签 2=欠费停服（实底 danger）
- [x] 2.3 错误码：gateway `insufficient_quota` → `insufficient_balance`；月限独立 `monthly_limit_exceeded`；member 接入文档 FAQ 同步
- [x] 2.4 定价双口径：format.ts +fmtPrice1K（元/千，4 位小数）；模型定价表单 tip 与列表单价列并列 `/百万 · /千`

## 3. 单月上限（4.7）

- [x] 3.1 计费路径：Settle 单语句 `CASE WHEN monthly_period = ? THEN +cost ELSE cost END` 原子累计（跨月首笔即重置，period 用账期时区 PeriodOf）；users/orgs 双记账同既有总额路径
- [x] 3.2 预检拦截：Precheck 读侧同口径（存储账期≠当前月视为 0），user/org 月限达限返回 ErrUserMonthly/ErrOrgMonthly（429 monthly_limit_exceeded）；0=不限
- [x] 3.3 设置入口：平台 UpdateOrg +monthly_quota（公司详情池卡输入器+本月已用读数）；org UpdateMember +monthly_quota（员工额度对话框并入"额度 / 月限"，独立设值提交）；GetAlertLevels 返回 monthly_quota/monthly_used（org 账单页月度读数条 + 进度条）
- [x] 3.4 前端展示：员工列表加"单月上限"列（0 显示 —，含本月已用折算元）；Org/Member 接口补 monthly_* 字段

## 4. 测试与收尾

- [x] 4.1 service/monthly_test.go：同月累计/员工月限拦截/跨月惰性清零（读侧+写侧原子重置）/org 月限独立拦截/0=不限/欠费全生命周期（耗尽→自动2→充值恢复→手动停用不误恢复）/org 月累计记账
- [x] 4.2 `go vet` 无告警；`go test ./internal/...` 全绿（含既有 gateway/api/service 回归）；`make build` 通过
- [x] 4.3 README 功能总览补条目（单月上限与欠费停服）
