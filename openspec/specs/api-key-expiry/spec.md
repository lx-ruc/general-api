# api-key-expiry Specification

## Purpose
TBD - created by archiving change api-key-expiry. Update Purpose after archive.
## Requirements
### Requirement: 创建时设定有效期
员工创建 API key 时 SHALL 可选指定过期时刻（unix 秒，必须晚于当前时间）；未指定则为永久有效；列表 SHALL 展示过期状态徽标（已过期/剩余天数）。

#### Scenario: 设定有效期
- **WHEN** 员工创建 key 时选择 30 天后过期
- **THEN** 创建成功且响应回显过期时刻；30 天后该 key 调用返回 401

#### Scenario: 非法过期值
- **WHEN** 创建时 expires_at 为过去时刻
- **THEN** 返回 400 且不创建 key

#### Scenario: 过期 key 拒绝（既有逻辑 spec 化）
- **WHEN** 已过期 key 发起 /v1 请求
- **THEN** 返回 401 invalid_api_key

