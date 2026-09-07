# 设计：设值路径入流水

## Context

设值入口三处：`platform/handler.go` UpdateOrg（SET 绝对值）、`org/handler.go` 编辑员工额度（SET）、同文件置 NULL 分支；另 org/user 创建带初始额度也不入流水。追加式入口已入流水。

## Decisions

### D1：差值语义，同事务
设值改为：同事务内读旧值 → SET 新值 → `INSERT quota_grants(amount = 新−旧, remark='设值调整', operator_id)`；置 NULL 记 `amount = −旧值`。差值可正可负，quota_grants.amount 本无正数约束。建号初始额度记 `remark='初始额度'`。

### D2：不变量
**从建号至今 Σ(grants) = 当前 quota_limit** 恒成立（任意时刻 limit 可由流水+初值重建）。

### D3：并发
SQLite `_txlock=immediate` 单写者串行化已护住读旧值→SET 窗口；PG 模式同事务内 UPDATE 前先 SELECT FOR UPDATE 或直接以 `UPDATE ... RETURNING` 取旧值。三处改造保持同一事务边界。

## Risks / Trade-offs

- [设值与追加语义混入同一流水表] → remark 区分（设值调整/初始额度 vs 常规备注）；对账只关心差值序列
- [遗漏某条写 limit 的路径] → 以 grep `quota_limit` 全仓复核为收尾门禁

## Migration Plan

纯 handler 改造，无 schema 变更；部署即生效；历史流水缺口无法补录（对账单已用快照方案绕开）。
