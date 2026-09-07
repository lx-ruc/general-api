## ADDED Requirements

### Requirement: 周期渠道探活
系统 SHALL 按 `gateway.channel_test_interval`（0=关闭）周期对 `status=1` 的渠道发起探活请求（复用渠道测试逻辑，含模型映射），并暴露 `tg_channel_probe_result_total` 指标（result=ok/fail）。

#### Scenario: 间隔为零不探活
- **WHEN** `channel_test_interval` 为 0（默认）
- **THEN** 不启动探活 goroutine，行为与现状一致

#### Scenario: 探活成功无副作用
- **WHEN** 渠道上游正常，探活请求返回 2xx
- **THEN** 指标 result=ok 计数 +1，渠道状态与备注不变

### Requirement: 连续失败自动禁用
探活连续失败达到阈值（默认 3）时，系统 SHALL 将渠道 `status=0` 并写备注"定时探活连续失败"；成功一次即清零计数；禁用后可经现有渠道管理手动恢复。

#### Scenario: 厂商抖动不禁用
- **WHEN** 探活失败 1 次后下一次成功
- **THEN** 计数清零，渠道保持启用

#### Scenario: 连续失败达阈值
- **WHEN** 同一渠道连续 3 次探活失败
- **THEN** 渠道 status=0、备注含探活失败字样、指标 result=fail 累计；后续请求不再选中该渠道

### Requirement: 探活多节点独立
多实例部署时各节点 SHALL 独立探活（不做跨节点协调）；重复探活幂等，禁用写库收敛到同一结果。

#### Scenario: 双节点同时探活
- **WHEN** 两节点均在探活窗口内对同一渠道探活且均失败达阈值
- **THEN** 渠道被禁用一次，备注不重复叠加
