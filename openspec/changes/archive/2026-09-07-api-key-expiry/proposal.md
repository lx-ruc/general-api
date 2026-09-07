# 唤醒：API key 过期时间的创建入口

## Why

one-api 对比时把"令牌过期"标为我方差距，钻取发现实为**沉睡半成品**：`api_keys.expired_at` 列已存在、`apiauth.go:60` 已强制过期拒绝，唯独 CreateKey 不收该参数、前端无入口。唤醒成本 XS。

## What Changes

- member `CreateKey` 请求体加可选 `expires_at`（unix 秒；> now 校验，缺省=永久）
- `MyKeysView.vue` 创建对话框加过期日期选择器；key 列表加"已过期/剩余天数"徽标
- 过期拒绝逻辑已存在，不动

## Capabilities

### New Capabilities
- `api-key-expiry`: 创建时设定有效期与过期展示

### Modified Capabilities
<!-- 无存量 spec -->

## Impact

- `internal/api/member/handler.go`（一个参数+校验）
- `web/src/views/member/MyKeysView.vue`、`web/src/api/member.ts`
- 测试：过期时间校验（过去时刻拒绝）、过期 key 调用 401（已有逻辑补 spec 化用例）
