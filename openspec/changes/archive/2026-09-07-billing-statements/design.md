# 设计：月度对账单 + 渠道成本对账

## Context

usage_logs append-only（闭账期天然冻结）；价格/成本中心均结算快照；quota_grants.amount 无正数约束（负 grant 即冲账）。钻取发现设值路径绕流水（quota-audit-fix 另案），故期初不走"倒推"走"月末快照"。

## Goals / Non-Goals

**Goals:** 口径三声明 + 三段式账单 + 链式勾稽 + CSV 导出 + 厂商账单差异报表
**Non-Goals:** PDF（CSV 先行，会计系统要的是 CSV）；预聚合 rollup（量级不需要，逃生舱记录在案）；digest 摘要（属 budget-alerts）

## Decisions

### D1：口径三声明（印在账单头）
归期=结算完成时刻（跨月流式归次月）；月边界按 `billing.timezone`（默认 Asia/Shanghai，配置 `billing.timezone`，实现时目标时区算 wall-clock 再转 unix 比较）；计价=全整数点数 + settings.points_per_yuan 汇率 + 价格取结算快照（改价不影响已出账单）。

### D2：期初来源 = 消耗侧先行 + 月末快照（A+B），否决倒推（依赖被设值旁路破坏）与补流水（另案）
```
period_balances(org_id, period 'YYYY-MM', quota_limit, quota_used, snapshot_at)
月末 00:05 cron 写上月末值；settings CAS 抢跑（UPDATE settings SET value=:period WHERE key='balance_snapshot_period' AND value<>:period）
勾稽链式自证：snapshot[n+1].期初 == snapshot[n].期末；断裂标 ✗ 不静默
上线首月末起勾稽段生效；历史月份期初显示"—"
```

### D3：冲账 = 负数 grant，不建新表
`AddOrgQuota` 放开负值（仅平台管理员），remark 记事由；账单"冲减段"= 账期内 amount<0 的 grants。动额度 + 有审计 + 账单可见，一事三毕。

### D4：视角隔离（毛利防泄漏）
org 视角：售价口径，严禁 vendor_cost/cost_*_price/毛利（已 grep 验证现有 org/member 接口干净）。平台视角：任意 org + 毛利列。

### D5：CSV 细节
UTF-8 带 BOM（Excel 中文乱码第一坑）；末行写汇总行；文件名 `对账单_{org}_{YYYY-MM}.csv`。

### D6：vendor_bills 差异报表
```sql
vendor_bills(period 'YYYY-MM', channel_id, billed_points, note, created_by, created_at)
```
手工录入厂商账单 → 差异 = Σ我方 vendor_cost（按 channel）− 录入值；偏差率 >2% 标红；可下钻。**no_usage 行的漏损在此货币化**（我方记 0、厂商实收），为 P1-7 估算兜底之争提供数据。

### D7：性能
单 org 月聚合（org_id+created_at 复合索引，无则补）亚秒级；导出低频按钮，不建预聚合。千万行级再上日汇总 rollup（记录，不做）。

## Risks / Trade-offs

- [cron 漏跑断链] → 勾稽断裂显式标 ✗；补跑工具（手动触发快照端点）
- [厂商账期与我方口径错位] → 录入时显示我方口径统计值供人工比对；差异报表标注口径
- [时区配置错误] → 账单头印时区；边界计算单测覆盖

## Migration Plan

1. 建表/索引（幂等）；2. 部署即开始积累快照；3. 首月末后可出完整勾稽账单；回滚残留无害。
