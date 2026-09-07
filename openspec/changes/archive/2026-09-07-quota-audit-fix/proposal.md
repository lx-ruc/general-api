# 修复：额度"设值"路径绕过审计流水

## Why

billing-statements 钻取中发现：CLAUDE.md 宣称"额度入口只有 AddOrgQuota/AddUserQuota（带 QuotaGrant 流水）"，但**直接设值**路径并存且不记流水——`platform/handler.go:100`（编辑 org 额度 `SET quota_limit=?`）、`org/handler.go:106`（编辑员工额度 SET）、`org/handler.go:179`（置 NULL）。后果：quota_limit 历史序列不可重建，审计链有旁路。

## What Changes

- 三处设值改为**同事务差值入流水**：读旧值 → `SET quota_limit = 新值` → `INSERT quota_grants(amount = 新−旧, remark='设值调整', operator)`；置 NULL 记 `amount = −旧值`
- org/user 创建时的初始额度同样入流水（remark='初始额度'），使"从建号至今 Σgrants = 当前 limit"恒成立
- 追加式入口（AddOrgQuota/AddUserQuota）不动；API 请求/响应行为零变化（纯审计增强）

## Capabilities

### New Capabilities
- `quota-audit`: quota_limit 一切变更均有差值流水的审计不变量

### Modified Capabilities
<!-- 无存量 spec -->

## Impact

- `internal/api/platform/handler.go`（UpdateOrg 设值分支、CreateOrg 初始额度）
- `internal/api/org/handler.go`（UpdateMember 设值分支、置 NULL 分支、成员创建初始额度）
- 测试：设值/置空/建号各产生一条差值流水；Σgrants 重建现值；追加路径不受影响
- 无 schema 变更（quota_grants.amount 本就无正数约束）
