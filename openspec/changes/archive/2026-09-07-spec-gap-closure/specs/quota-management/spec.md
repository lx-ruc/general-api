## ADDED Requirements

### Requirement: 单月消费上限
系统 SHALL 支持为公司（平台设置）与员工（公司管理员设置）配置单月消费上限（token 预算，0=不限）；当月累计消费达上限后新请求 MUST 被拒绝并返回 `monthly_limit_exceeded`；跨月后累计 MUST 自动清零恢复（结算路径单语句原子重置，读路径惰性取值），无需定时任务。

#### Scenario: 当月达限拦截
- **WHEN** 员工月限 10,000 点且本月已结算 10,000 点，发起新请求
- **THEN** 返回 429 `monthly_limit_exceeded`，请求不打上游

#### Scenario: 次月自动清零
- **WHEN** 月限达限后进入次月，同配置不变
- **THEN** 新月首笔请求通过，月累计从该笔重新起算

### Requirement: 欠费停服状态
公司 status SHALL 支持 `2=欠费停服`：总额度耗尽的结算同事务自动置 2（仅从 1 迁移），数据面调用返回 403 `insufficient_balance`（欠费文案）；平台追加额度产生余量时 SHALL 自动恢复 1；手动停用（0）MUST NOT 被自动恢复或自动停服覆盖。

#### Scenario: 耗尽自动停服
- **WHEN** 结算使公司 quota_used ≥ quota_limit（原状态 1）
- **THEN** status 自动置 2，后续 /v1 调用 403

#### Scenario: 充值自动恢复
- **WHEN** 平台追加额度使 quota_limit > quota_used（原状态 2）
- **THEN** status 自动恢复 1，调用恢复

### Requirement: 开户联系字段
公司创建/编辑 SHALL 支持联系人姓名与联系电话；列表与详情 SHALL 展示。

#### Scenario: 开户带联系人
- **WHEN** 平台创建公司并填写联系人/电话
- **THEN** 公司列表与详情可见，可后续修改
