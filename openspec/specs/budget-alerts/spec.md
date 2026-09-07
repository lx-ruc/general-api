# budget-alerts Specification

## Purpose
TBD - created by archiving change budget-alerts. Update Purpose after archive.
## Requirements
### Requirement: 边沿告警状态机
系统 SHALL 在额度使用率跨越配置阈值时发送一次告警邮件（每次跨越一封）；使用率停留在阈值以上 MUST NOT 重复发送；单笔请求跨多档时 MUST 只按最高档发送一封。

#### Scenario: 跨越即发且仅发一次
- **WHEN** org 使用率从 79% 经一笔请求到 81%，随后多笔请求维持 85%
- **THEN** 恰好发送一封 80% 预警邮件，后续不再发送

#### Scenario: 跳档收敛
- **WHEN** 单笔大请求使使用率从 79% 直接到 100%
- **THEN** 仅发送耗尽告警，不额外发 80% 预警

### Requirement: 惰性复位与再告警
追加额度使使用率回落时，系统 SHALL 在下次检查时静默降级档位（不发送邮件）；此后再次跨越阈值 SHALL 重新告警；回落到阈值与 100% 之间的中间档时 MUST NOT 立即重发预警。

#### Scenario: 追加后再告警
- **WHEN** org 100% 耗尽告警后平台追加额度使使用率回到 60%，之后再次爬到 80% 与 100%
- **THEN** 两次跨越各重新告警一次

#### Scenario: 拨到中间档不重发
- **WHEN** 追加额度使使用率从 100% 回到 85%（仍 ≥80%）
- **THEN** 不发送预警邮件；再次耗尽时会发送耗尽告警

### Requirement: 竞态唯一性
并发结算同时检测到跨越时，系统 MUST 只发送一封邮件（数据库条件更新定胜负），多实例部署同样成立。

#### Scenario: 并发单发
- **WHEN** 50 笔并发请求结算同时使使用率越线
- **THEN** 恰好一封告警邮件

### Requirement: 收件扇出
org 级告警 SHALL 发送给该 org 全部 org_admin；org 耗尽附加通知平台管理员；user 级告警发送给员工本人（有邮箱时）与其 org_admin；`alert_levels` 为空数组的主体 MUST NOT 告警。

#### Scenario: org 耗尽通知平台
- **WHEN** org 使用率达 100%
- **THEN** org_admin 收到耗尽邮件，平台管理员同时收到通知

### Requirement: 数据面零侵入
告警检查 MUST 在结算提交后异步执行，主请求路径不因告警增加同步数据库操作或邮件发送等待。

#### Scenario: 高频调用无感
- **WHEN** 某主体 1 秒内发生 100 笔结算
- **THEN** 告警检查因节流至多执行一次，请求延迟无可测增加

