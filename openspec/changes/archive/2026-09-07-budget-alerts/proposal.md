# 预算告警：额度阈值的边沿告警状态机

## Why

额度耗尽今天只有被动 429（用户侧碰壁才知道）；水位预警邮件是 B2B 管理者的刚需，one-api 也没有。org 打满额度对平台还是续费/追加的商业时刻。

## What Changes

- `orgs`/`users` 各加三列：`alert_levels`（JSON 阈值数组，默认 `[80]`，`[]`=关）、`alert_level`（**整数档位状态机**：已告警的最高档，0=未告警）、`alert_since`（进入当前档时刻）
- 告警状态机：**边沿触发**（跨越才发、每次跨越一封、跳档只发最高档）+ **惰性复位**（追加额度后下次检查静默降级，无需给拨备代码加钩子）
- 检查点：Settle 提交后异步 goroutine（复用 last_used_at 惯用法）+ per-subject 60s 节流；跨阈值时 CAS 抢占（`UPDATE ... WHERE alert_level < :new`），RowsAffected=1 的胜者发信——多实例安全，无 Redis
- 收件扇出：org 级 → 全部 org_admin；org 耗尽附加通知平台管理员（续费线索）；user 级 → 员工本人（有邮箱时）+ 其 org_admin
- 指标 `tg_budget_alerts_total{subject,level}`、`tg_alert_mail_fail_total`
- 每日摘要 digest：**设计保留、本期不实现**（cron + settings CAS 抢跑积木已备，见 design）

不改：Precheck/Settle 主路径（检查全异步，数据面零额外同步查询）。

## Capabilities

### New Capabilities
- `budget-alerts`: 阈值配置、边沿告警状态机、竞态唯一性、收件扇出与性能边界

### Modified Capabilities
<!-- 无存量 spec -->

## Impact

- `internal/database/schema.sql` + `migrate.go`：orgs/users 各 3 列
- `internal/service/quota.go`（Settle 提交后挂异步检查）+ 新 `internal/service/alerts.go`（状态机/节流/CAS/扇出）
- `internal/service/notifier.go` 复用发送；`internal/metrics/metrics.go` 两指标
- org/platform 端阈值编辑接口（编辑时静默重算 alert_level）
- `web/src/views/org|platform/`：阈值设置 UI
- 测试：转移表每行一用例（含 79→100 跳档收敛、拨备到中间档不重发、竞态单发）
