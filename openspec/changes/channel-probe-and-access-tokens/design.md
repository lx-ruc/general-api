# 设计：定时渠道体检 + 管理面访问令牌

## Context

两个独立但同属"运维自主化"的能力。探活补的是熔断的盲区（无流量渠道无失败信号）；访问令牌补的是管理面只有人类入口的缺口。两者均不触碰数据面与计费不变量。

## Goals / Non-Goals

**Goals:**
- 探活：周期性主动验证渠道可用性，连续失败自动禁用并可告警
- 访问令牌：程序化管理面调用，权限语义与网页登录完全等价

**Non-Goals:**
- 探活不做 Redis 协调（每节点独立探活，幂等浪费可接受）
- 令牌不做 scope 细分（v1 令牌=属主全权限；`read_only` 标志留给 v2）
- 不做上游余额轮询（独立小项，另行提案）
- 不做 WebHOOK/Message Pusher 外发（告警走现有 smtp + /metrics）

## Decisions

### D1：探活复用 TestChannel 核心，取渠道第一条 ability 模型

从 `platform/handler.go` 的 TestChannel 抽出"给定渠道+模型发一次请求并判定"的纯函数，探活与测试按钮共用。探活模型取 `channel_abilities` 第一条（ORDER BY id），不区分 chat/embedding 端点。注意：若 generalize-data-plane 已落地，此处需应用 model_mapping。

### D2：连续 M 次失败才禁用，计数在内存

探活失败计数 per-node 内存（与渠道熔断计数同款取舍）：连续 ≥3 次失败 → `status=0` + 备注"定时探活连续失败"。成功一次清零。探活间隔默认 30m、失败判定窗口由配置控制；探活请求产生的厂商费用 = 一次 mini chat（预置模型单价 0 时为 0）。多实例下 N 节点各探各的：重复探活幂等无害，禁用写库天然收敛。

**备选**：Redis 分布式锁单节点探活——否决，为省几次探活调用引入协调依赖不值。

### D3：访问令牌复刻 api_keys 的 SHA-256 模式，`tgp_` 前缀

- 表 `access_tokens(id, user_id, name, token_hash UNIQUE, status, expires_at, last_used_at, created_at)`；明文 `tgp_` + 32 字节随机，仅创建时显示一次
- 前缀互斥判别：JWT 以 `eyJ` 开头（base64 头），API key `sk-`，访问令牌 `tgp_`——中间件无需额外探测
- `JWTAuth` 中间件入口改双轨：`tgp_` 走 hash 查表 → 载入用户 → 与 JWT 完全相同的 context 注入，**RBAC/org 隔离链路零改动**（令牌即属主）
- `last_used_at` 节流：距上次 >60s 才 UPDATE，避免每请求写库
- 过期：`expires_at` 非零且过期 → 401（懒判，不做清扫任务）

### D4：令牌 CRUD 挂在"个人设置"，按属主过滤

平台管理员与公司管理员都能给自己发令牌（权限由 RBAC 自然区分，公司管理员令牌只能操作本公司——org 隔离 WHERE 已是 handler 铁律）。列表/吊销按 `user_id = 当前用户` 过滤，不能看到别人的令牌。

## Risks / Trade-offs

- [令牌泄漏 = 完整管理权限] → 文档明示；`tgp_` 前缀便于 secret 扫描器识别；吊销即时生效（每请求查库，无缓存）；v2 加 read_only
- [探活误杀（厂商抖动）] → 连续 3 次才禁用（3×30m 窗口）；禁用写备注可人工恢复；探活失败不禁用 key 只禁渠道
- [探活费用] → 预置模型单价 0；定价模型探活一次成本个位数点，30m 间隔可忽略
- [令牌与 JWT 双轨增加中间件复杂度] → 判别仅看前缀，分支互斥，测试覆盖三条路径

## Migration Plan

1. 新表 `access_tokens`（schema.sql + migrate.go 建表，幂等）
2. 部署后管理员按需在设置页创建令牌；探活配置 `channel_test_interval: 30m` 后生效
3. 回滚：二进制回退；表残留无害

## Open Questions

- 探活失败是否同时邮件告警：v1 接现有 smtp（配置了就发），默认仅日志+指标
- 令牌数量上限：暂不设（吊销自理），观察滥用再议
