# 运维自主化：定时渠道体检 + 管理面访问令牌

## Why

对标 one-api 的差距分析（2026-09-07）确认两个 P0 运维缺口：一是低流量渠道的故障只能靠流量触发的熔断发现（没有流量就没有失败信号），哑渠道会静默留存直到客户踩雷；二是管理面只有网页 JWT，客户/CI 想自动化对接（开户、授权、查用量）没有程序化入口，one-api 有完整的管理 API access token 体系。

## What Changes

- 后台定时渠道体检：`gateway.channel_test_interval`（默认 0=关，建议 30m）周期对启用渠道发起探活请求；连续 M 次（默认 3）失败自动 `status=0` 并写备注"定时探活失败"，可选邮件告警；新增 `tg_channel_probe_result_total` 指标
- 管理面访问令牌：新表 `access_tokens`（SHA-256 哈希存储、明文仅创建时显示一次，复刻 api_keys 模式）；`tgp_` 前缀与 JWT/`sk-` 格式互斥；JWT 鉴权中间件双轨——`Bearer tgp_...` 查令牌载入用户后走完全相同的 RBAC/org 隔离链路
- 个人设置页（平台与公司管理员）增加"访问令牌"管理：创建/命名/显示一次/吊销；`last_used_at` 节流更新（>60s 才写）
- 多实例语义：各节点独立探活（幂等，重复探活无害）；访问令牌全库共享天然多实例安全

不改：现有熔断逻辑（探活是补充信号源）、数据面鉴权（API key 路径零改动）、RBAC 权限矩阵（令牌权限=属主用户权限）。

## Capabilities

### New Capabilities
- `channel-probe`: 定时渠道探活的调度、判定、禁用与告警行为
- `admin-access-tokens`: 管理面访问令牌的签发、鉴权与吊销

### Modified Capabilities
<!-- openspec/specs/ 当前为空，无存量 capability 需要修改 -->

## Impact

- `main.go`：探活 goroutine 启动与优雅退出
- `internal/api/platform/handler.go`：抽出 TestChannel 可复用的发送+判定核心
- `internal/middleware/`：JWTAuth 双轨扩展
- `internal/database/schema.sql` + `migrate.go`：`access_tokens` 新表（含 token_hash 唯一索引）
- `internal/model/`：AccessToken 实体
- `internal/api/{platform,org}/`：令牌 CRUD（或独立 profile 子包）；`internal/metrics/metrics.go`：探活指标
- `web/src/views/`：个人设置页令牌区块；渠道列表展示探活备注
- 测试：探活连续失败禁用（tick 模拟）；令牌鉴权双轨、吊销即失效、RBAC 随属主
