# quota-audit Specification

## Purpose
TBD - created by archiving change quota-audit-fix. Update Purpose after archive.
## Requirements
### Requirement: 设值入流水
quota_limit 的一切变更（设值、置空、建号初始额度、追加）MUST 产生对应差值流水（amount = 新值 − 旧值，可负），使 Σgrants = 当前 limit 恒成立；API 请求与响应行为不变。

#### Scenario: 设值产生差值流水
- **WHEN** 平台管理员把 org 额度从 500 万点设为 300 万点
- **THEN** quota_grants 新增 amount=−2,000,000、remark 含"设值调整"的流水，org.limit 变为 300 万

#### Scenario: 建号初始额度入流水
- **WHEN** 创建 org 并分配初始额度 100 万点
- **THEN** quota_grants 出现 amount=1,000,000 的初始流水，此后 Σgrants 等于 limit

#### Scenario: 追加路径不受影响
- **WHEN** 平台管理员经追加入口 +50 万点
- **THEN** 行为与流水与改造前一致

