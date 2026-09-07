# 设计：API key 过期入口

## Context

沉睡半成品：`api_keys.expired_at` 列 + `apiauth.go` 过期强制拒绝均已存在，仅 CreateKey 不收参数、前端无入口。

## Decisions

- CreateKey 请求体加可选 `expires_at`（unix 秒）；校验 `expires_at > now`，非法值 400；缺省/0 = 永久（现状语义）
- 中间件拒绝逻辑不动（已有：`ExpiredAt != nil && *ExpiredAt <= now` → 401）
- 前端：创建对话框日期选择器（快捷：7天/30天/90天/永久）；列表按剩余天数显示徽标（已过期=红、≤7天=橙）
- key 无编辑端点（创建/吊销模型），过期后不自动改 status——懒判即可，列表徽标已可读

## Risks / Trade-offs

- [员工误设过短过期] → 创建成功提示里显式回显有效期

## Migration Plan

无 schema 变更；部署即生效。
