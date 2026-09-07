# 月度对账单导出 + 渠道成本对账

## Why

四件套收口件：财务要月度账单核对，平台要向客户证明"精确计量"（毛利白盒的最后一环）。同时把 `no_usage` 漏损从理念争论变成每月一个货币化数字。

钻取中发现的前置事实：额度"设值"路径（直接 `SET quota_limit`）绕过 quota_grants 流水，limit 历史不可重建——期初余额因此改用**月末快照表**方案（审计旁路修复另立 quota-audit-fix）。

## What Changes

- **口径三声明**印在每张账单头：结算完成时刻归期（跨月流式归次月）、月边界按 `billing.timezone`（默认 Asia/Shanghai）、全整数点数 + `points_per_yuan` 汇率 + 价格取结算快照
- 月度对账单三段式：额度勾稽段（期初 + 追加 − 冲减 − 消耗 = 期末，**链式勾稽**：下月期初=本月期末，断裂标 ✗）／冲减明细段（负 grant）／用量明细段（模型 × 成本中心 × 日矩阵，缓存命中单列）
- 新表 `period_balances(org_id, period, quota_limit, quota_used)`：月末 00:05 cron 快照（settings CAS 抢跑防多实例双写）；上线首月末起勾稽段生效，历史月份期初显示"—"
- 冲账 = **负数 grant**（`AddOrgQuota` 放开负值给平台管理员 + remark）——不建 billing_adjustments 新表
- CSV 导出：UTF-8 **带 BOM**、末行汇总、文件名 `对账单_{org}_{YYYY-MM}.csv`；org 视角**严禁** vendor_cost/毛利（已验证现有接口干净，保持）
- 新表 `vendor_bills(period, channel_id, billed_points, note)` 手工录入厂商账单；差异报表 = 我方 vendor_cost 聚合 vs 录入值，**偏差 >2% 标红**，可下钻构成明细

## Capabilities

### New Capabilities
- `billing-statements`: 账期口径、三段结构、勾稽链、冲减展示、视角隔离与导出格式
- `vendor-reconciliation`: 厂商账单录入与差异报表

### Modified Capabilities
<!-- 无存量 spec -->

## Impact

- `internal/database/schema.sql` + `migrate.go`：2 新表 + usage_logs 补 `(org_id, created_at)` 复合索引（若无）
- `internal/service/`：对账单聚合查询（stats.go 模式扩展）、月末快照 cron、`AddOrgQuota` 负值放开
- `internal/api/org/`（本 org 账单导出）、`internal/api/platform/`（任意 org 账单含毛利、vendor_bills 录入与差异报表）
- `web/src/views/org|platform/`：账单页 + 厂商对账页
- 测试：勾稽链自洽/断裂检测、跨月流归期、时区边界、org 视角 CSV 无毛利字段、BOM 存在性、差异标红阈值
