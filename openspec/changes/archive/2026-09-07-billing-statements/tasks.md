## 1. 数据层与口径

- [x] 1.1 schema.sql + migrate.go：period_balances、vendor_bills 两表；usage_logs 确认/补 (org_id, created_at) 复合索引
  - `schema.sql` 新增两表（含注释语义：period_balances UNIQUE(org_id,period)；vendor_bills UNIQUE(period,channel_id)）；新表靠启动幂等执行 schema.sql 在存量库自动建出，无需 alters
  - `model/billing.go` 新增 PeriodBalance/VendorBill；`service/migrate_sqlite.go` migrateTables 补两表（切 PG 搬数据）
  - idx_logs_org(org_id, created_at) 已存在，确认无需补
- [x] 1.2 config.go：billing.timezone（默认 Asia/Shanghai）+ TG_BILLING_TIMEZONE；账期边界工具函数（目标时区 wall-clock → unix）
  - `config.go` Billing{Timezone} 默认 "Asia/Shanghai"，TG_BILLING_TIMEZONE；router.go 启动注入 `service.SetBillingTimezone`
  - `service/billing.go`：BillingLocation（非法名回退 SH→UTC）、PeriodBounds（ParseInLocation+AddDate，服务器本地时区无关）、PrevPeriod、PeriodOf
- [x] 1.3 月末快照 cron（00:05 写上月末 limit/used；settings CAS 抢跑）+ 手动补跑端点
  - `RunBalanceSnapshotter`：启动即自愈补跑一次 + 每月 1 日 00:05（账期时区）sleep 循环；settings CAS `balance_snapshot_period`（value<>prev 才写，多实例唯一写入者）；SnapshotBalances UPSERT `ON CONFLICT(org_id,period) DO UPDATE`（SQLite/PG 同句式，可重复补跑幂等）
  - 手动补跑：POST /api/platform/billing/snapshots {period}（platform/statement.go RunSnapshot，测试覆盖写入行数）

## 2. 冲账与账单

- [x] 2.1 AddOrgQuota 放开负值（仅平台管理员入口）+ UI 冲减入口
  - AddOrgQuota/UpdateOrgQuota 校验改为 amount != 0（负值即冲减，QuotaGrant 流水负数入账自动归冲减段）；拒绝扣穿剩余额度
  - UI：OrgListView 追加额度对话框改名「追加 / 冲减额度」，金额允许负数并提示"负数为冲减回收，入对账单冲减段"+ 事由备注
- [x] 2.2 对账单聚合服务：勾稽段（快照期初/grants 分正负/消耗Σ/链式校验断裂标✗）+ 冲减段 + 明细段（模型×成本中心×日，缓存命中单列）
  - `BuildBillStatement`：期初=snapshot[M-1]（缺失→OpeningMissing 显示"—"、ChainOK=nil 无法勾稽）；期末=snapshot[M] 或当月实时值（ClosingIsLive 标注）；grants 正负分桶 Grants/Revokes；消耗=Σcost+no_usage 笔数披露；链式校验 `期末used−期初used==消耗`，断裂显式标 ✗（不静默）
  - 明细段日分桶可移植 TZ 方案：Go 算月内固定偏移秒 `_, off := time.Unix(s,0).In(loc).Zone()`，SQL `strftime('%Y-%m-%d', l.created_at+N,'unixepoch')` / PG `to_char(to_timestamp(...))`，不依赖服务器 localtime；JOIN cost_centers 须限定 `l.`（双方都有 created_at 踩过 ambiguous column）；`ORDER BY (l.cost_center_id IS NULL)` 未归集置底
- [x] 2.3 org 账单端点 + CSV 导出（BOM/汇总行/文件名）；平台任意 org 账单端点（+毛利列）
  - GET /api/org/billing/statement[+/csv]（includeVendor=false）；GET /api/platform/orgs/:id/statement[+/csv]（includeVendor=true，明细多厂商成本/毛利列）
  - 视图隔离双保险：includeVendor=false 时 SQL 不查 vendor_cost，且 BillDetailRow.VendorCost/Margin `omitempty`（JSON 0 值不出现，测试断言）；WriteStatementCSV 三段式（勾稽/冲减/明细+合计行）、UTF-8 BOM、文件名 `对账单_{org}_{YYYY-MM}.csv`（RFC5987 filename* + url.PathEscape）
- [x] 2.4 前端：org 账单页（选月/预览/导出）；platform 账单页
  - org BillingView 新增「账单勾稽」卡：el-descriptions 九宫格（期初/链式校验 ✓✗—/授权/冲减/消耗/期末+实时 tag/不计量笔数）+ 冲减段表 + 明细段表（日期/模型/中心/请求/缓存命中/入出tokens/金额，未归集置底 dim）+「导出三段式账单 CSV」按钮（blob 下载，旧客户端汇总导出改名区分）
  - platform OrgDetailView 新增「月度对账单」卡：同三段式，明细含营收/厂商成本/毛利列（毛利负数红色），选月+导出
- [x] 3.1 vendor_bills 录入/改写端点 + 前端页（本项与 3.2 合并实现）
  - PUT/DELETE /api/platform/vendor-bills（UPDATE-then-INSERT 事务 Upsert）；GET 合并 usage GROUP BY channel_id 与录入账单
  - 前端 VendorBillView.vue 新页：账期选择（近 12 月）+ 录入 dialog（渠道/账单点数/备注，显示折合元）+ 差异表；路由 /platform/vendor-bills + 侧栏菜单「厂商对账」

## 3. 渠道对账

- [x] 3.2 差异报表：Σvendor_cost by channel vs 录入值，>2% 标红，下钻含 no_usage 笔数
  - VendorDiffRow：OurCost（我方 Σvendor_cost）/Billed/Diff/DiffPct=|diff|*100/billed/OverPct(>2%)/NoUsageCount/HasBill；未录入显示"—"不参与标红；按 OurCost+Billed 降序
  - 前端偏差率红色加粗、超差行整行浅红底；no_usage>0 橙色；测试 TestVendorBillDiff 断言 diff 1000/diff_pct 11/over true/false/no_usage 2

## 4. 测试与收尾

- [x] 4.1 测试：跨月流归期、时区边界（UTC 服务器下 8 月边界正确）、勾稽自洽与断裂、负 grant 联动、org CSV 无毛利字段、BOM 存在、差异标红
  - `service/billing_test.go`：PeriodBoundsTimezone（UTC 直界 + SH=07-31T16:00Z + 非法报错）、CrossMonthAttribution（09-01 00:00 SH 归 9 月，-1s 归 8 月）、StatementChain（快照幂等/勾稽自洽/篡改断链✗/当月实时期末/首月前期初缺失）、CSVisolation（BOM+org 无厂商列含 JSON omitempty+platform 有）、DetailOrdering（未归集置底）
  - `platform/handler_test.go` TestVendorBillDiff（has_bill false→录入→差异/偏差率/标红/no_usage/补跑快照）
  - 踩坑记录：SQLite 拒绝 Go 风格 `1_400_000` 下划线字面量且错误被吞——SQL 内一律纯数字并检查 err
- [x] 4.2 `go test ./internal/...` 全绿；`go vet ./...`；`make build` 通过
  - 全部 ok（api/org、api/platform、coord、gateway、service）；vet 无告警；vite build + go build 单二进制通过
- [x] 4.3 README/docs 增补（口径说明 + 使用流程）
  - README 功能总览 +「月度对账单」条目（三段式/快照次月生效与自愈/勾稽链/冲减/CSV BOM/视图隔离/厂商对账）；config.example.yaml 补 billing.timezone 注释段（含补跑与自愈说明）；账单头印口径三声明（归期=结算完成时刻/月边界=账期时区/计价=整数点数结算快照价）
