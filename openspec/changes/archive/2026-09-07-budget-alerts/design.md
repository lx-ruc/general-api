# 设计：预算告警

## Context

`quota_used` 单调不减、limit 只经拨备/设值变化——单调性是状态机简化的根基。多实例无 Redis；DB 行即协调器。notifier.go 邮件基建可复用；org_admin 必有邮箱（注册强制），member 可能空。

## Goals / Non-Goals

**Goals:** 边沿告警状态机（整数档位）+ 竞态唯一 + 收件扇出 + 数据面零侵入
**Non-Goals:** 每日摘要 digest（设计见 D7，本期不做）；月度成本中心预算（聚合只配告警不配拦截，且等真实信号）；三档 UI（schema 已支持，v1 只开两档）

## Decisions

### D1：位域坍缩为整数档位
不变量"位从低位连续填满"（进 100% 必过 80%）⇒ 状态就是"已告警的最高档"一个整数：
```
bracket(r) = max{i : r ≥ t_i}   （t 数组升序，100% 恒为隐含末档）
new > alert_level → 告警 new 档（跳档只发最高）
new < alert_level → 静默降级（惰性复位）
new == alert_level → 无动作
```
三行规则覆盖 N 档，79→100 跳档收敛自动成立。

### D2：CAS 胜者发信，先置位后发信
```sql
UPDATE ... SET alert_level=:new, alert_since=:now WHERE id=:id AND alert_level < :new
```
RowsAffected=1 的协程/节点发信——并发 50 笔只有一封。先置位（去重确定性）后发信；SMTP 失败靠指标 + 耗尽时数据面 429 物理通知兜底。降级 UPDATE 无信，并发同值无害。

### D3：惰性复位，拨备不加钩子
复位不在线上做：下次 Settle 检查发现 ratio 掉头顺手降级。天然处理：拨到中间档（100%→85%）不重发预警（bit0 保留）但再耗尽会重新告警（bit1 已清）。改阈值时 UI 侧静默重算 alert_level（策略变更非水位事件）。

### D4：检查点与节流
Settle 提交后 `go checkBudgetAlert(...)`（复用 apiauth.go last_used_at 的 goroutine 惯用法）；进程内 `sync.Map[subject]lastCheck` 60s 节流（检测延迟 ≤60s 对邮件无感）。主路径零额外同步查询——**O(1) 计数器做拦截，异步边沿做告警**。

### D5：收件扇出
org ≥阈值/耗尽 → 全部 org_admin；org 耗尽附加平台管理员（续费线索）；user 级 → 员工本人（有邮箱时）+ 其 org_admin。quota_limit 为 NULL/0 不参与。

### D6：schema 按 N 档前瞻
`alert_levels TEXT DEFAULT '[80]'`（JSON 数组，`[]`=关）+ `alert_level INTEGER DEFAULT 0` + `alert_since INTEGER DEFAULT 0`。v1 UI 单阈值输入写 `[T]`；将来开三档纯前端改动。

### D7：digest 暂缓（积木已备）
每日摘要（org 版名单+老化天数 / 平台版续费线索日报）依赖：cron + `UPDATE settings SET value=:today WHERE key='digest_date' AND value<>:today` 抢跑。两块积木 v1 均已存在（后者模式与 CAS 同构），届时纯拼装。

## Risks / Trade-offs

- [SMTP 失败丢一封] → tg_alert_mail_fail_total + 日志 + 100% 时 429 兜底
- [节流窗口内跨越延迟] → ≤60s，邮件场景无感
- [多节点同时检查] → CAS 定胜负，DB 行即协调器

## Migration Plan

加列（幂等，默认值=现状"不告警"[80]但无历史水位问题——level=0 起步）；部署即生效；回滚残留无害。
