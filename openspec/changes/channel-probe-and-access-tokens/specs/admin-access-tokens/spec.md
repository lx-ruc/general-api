## ADDED Requirements

### Requirement: 访问令牌签发与管理
平台/公司管理员 SHALL 能在个人设置创建命名访问令牌（`tgp_` 前缀）；令牌明文仅创建时显示一次，库中只存 SHA-256 哈希（唯一索引）；支持列表（仅本人）与吊销。

#### Scenario: 创建并显示一次
- **WHEN** 管理员创建名为 "ci-deploy" 的令牌
- **THEN** 返回 `tgp_` 开头的完整明文（仅此一次），库中无明文，列表仅显示名称/状态/last_used_at

#### Scenario: 无法查看他人令牌
- **WHEN** 管理员请求令牌列表
- **THEN** 仅返回 user_id 为本人的令牌

### Requirement: 令牌鉴权与权限等价
管理面鉴权中间件 SHALL 同时接受 JWT 与访问令牌（`Authorization: Bearer tgp_...`）；令牌按属主用户身份走完全相同的 RBAC 与 org 隔离；吊销或过期后立即失效（无缓存）。

#### Scenario: 令牌调用管理 API
- **WHEN** 以有效 `tgp_` 令牌调用任意 `/api` 管理端点
- **THEN** 鉴权通过，权限与属主网页登录完全一致（公司管理员令牌只能操作本公司资源）

#### Scenario: 吊销即时失效
- **WHEN** 令牌被吊销后再次调用
- **THEN** 返回 401

#### Scenario: 过期令牌拒绝
- **WHEN** 令牌 expires_at 非零且已过期
- **THEN** 返回 401

### Requirement: 使用痕迹节流记录
系统 SHALL 在令牌被使用时更新 `last_used_at`，且节流为距上次记录超过 60 秒才写库。

#### Scenario: 高频调用不产生写放大
- **WHEN** 同一令牌 1 分钟内发起 100 次请求
- **THEN** last_used_at 至多更新 1~2 次
