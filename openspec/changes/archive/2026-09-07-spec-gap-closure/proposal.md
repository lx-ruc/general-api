# Proposal: spec-gap-closure（需求规格差距收口）

## Why

甲方《Token中转站需求规格》xlsx 34 项验收指标逐项对照后：29 项已实现，4 项字段/口径级小差距，1 项（4.7 单月上限）需实质开发。本变更一次性收口全部差距。

## What Changes

1. **开户联系字段（指标 1.2）**：orgs 加 `contact_name`/`contact_phone`；建司表单与列表/详情展示
2. **欠费停服状态（指标 1.5）**：org status 增加 `2=欠费停服`——Settle 同事务检测总额耗尽自动置 2，AddOrgQuota 有余量自动恢复 1（手动停用 0 不受影响）；数据面鉴权返回 403 insufficient_balance
3. **错误码对齐（指标 2.6/4.2）**：`insufficient_quota` → `insufficient_balance`；月限单独 `monthly_limit_exceeded`
4. **定价双口径展示（指标 4.5）**：模型定价表与表单并列展示 元/百万 与 元/千 token
5. **单月消费上限（指标 4.7，核心）**：orgs/users 加 `monthly_quota`（0=不限）+ `monthly_cost`/`monthly_period`；Settle 单语句 CASE 原子累计（跨月首笔即清零）；Precheck 读侧同口径判断，达限拦截；平台（公司详情）与 org（员工额度对话框）两个设置入口 + 读数展示

## Impact

- 风险面：Precheck/Settle 是计费不变量核心路径，月度三列为纯增量（0/'' 默认值不动既有行为）；欠费状态机只从 status=1 迁移，不碰手动停用
- 兼容：存量库经 alters 幂等加列，行为与未配置时完全一致
