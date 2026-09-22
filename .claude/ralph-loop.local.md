---
active: true
iteration: 9
session_id: a379c968-7f2d-491f-9af1-885267bb9c61
max_iterations: 20
completion_promise: "没有发现任何bug，所有功能全部可用，边界条件全部测到"
started_at: "2026-09-21T08:30:59Z"
---

全面测试整个系统，包括各种边界条件和并发、多账号登录、权限控制等，发现bug就修复

## 迭代 2（续）—— 2026-09-21 下午
- 多实例并发计费绝对不变量闭环：user.quota_used == org.quota_used == Σusage_logs.cost == 84000（420 成功 + 180 行 429/cost=0）；HA 测试环境已拆除（:8084/:8085、tg_ha_test 库、临时文件）
- SSE 无 usage：no_usage=1、cost=0、不扣费、include_usage 注入确认（N1-N3 10/10）；正向对照流式带 usage 精确扣 200（W1-W5 6/6）
- 注册/验证码/改密全流程 R1-R9：错码计数、5 次作废、过期、一次性消费、并发双注册、session_ver 改密即失效全过
- 【修复 a4692db】并发撞唯一约束泄漏驱动原始错误（注册 400 带原文/建子账号、建客户 500 带原文/建模型 500）→ database.IsDuplicateKey 四处映射友好文案；单测 3 个 + 真机三场景 6/6
- 生命周期矩阵 22 项：org 停用/恢复、欠费停服(2)管理台放行数据面拦、成员停用、key 删除/过期（expires_at 校验+到期即 401）、tgp_ 吊销即时 401、跨 org 重名拒绝
- 【修复 c18d081】internal/auth 零测试 → 8 组 JWT 用例；暴露缺口：手造缺 exp 的 token 永久有效 → WithExpirationRequired + WithIssuer 加固
- 【修复 c5d1e21】playground 1MB 自限 413 不排空 → urllib broken pipe（65acfbf 同类漏网点）；6MB 裸 socket 回归测试变异验证闭环
- Playground 流式计费 P1-P4 真机复验通过
- 收尾回归：go vet + 全部单测 + e2e 55/55 + vitest 15/15，全部通过

## 迭代 3 —— 2026-09-21 晚
- 模型映射全链路 live（M1-M10）：出站改写上游名（mock 日志实证）、非流式/流式每块改写回对外名、计费与 usage_logs 锚对外名、无上游名泄漏
- 多 Key 权重分布（P1-P4）：3 Key 8:1:1 → a≥24/40 全命中；优先级故障转移+熔断（F1-F6）：坏渠道恰 5 次触熔断 status=0/auto_disabled_at>0/备注，好渠道全接，账单全锚好渠道
- 欠费全周期（A1-A10）：超扣→org 自动 status=2→数据面 403 insufficient_balance→管理面不受影响→充值审批→自动恢复 status=1 立即可用；it3_test.py 55/55
- RBAC 矩阵（R1-R15）三角色互斥 403、跨 org 404、/v1/models 授权过滤、/metrics 指标与格式；代重置密码（it3b 13/13）：旧 JWT 即时失效、tgp_ 存活、API key 无关
- 【修复 4aefcd0】补 TestArrearsStateMachine：欠费状态机全边界迁移（1→2 / 2→1 / 手动 0 不迁移不误恢复 / limit=0 / cost=0）
- 【修复 fddb6de — 第 7 号 bug 族】模型下架(status=0)后数据面仍 200 计费：预检 handler.go:283 只查 name 不查 status（/v1/models 与 playground 均已过滤，仅数据面漏）→ 补 AND status=1，与不存在同文案不泄漏存在性；同文件连带两缺陷：UpdateModel 部分更新抹空 display_name/vendor/remark（值→指针语义）、cost_* 指针 binding:min=0 对 nil 必 400（补 omitempty）；回归 TestDisabledModelRejected + e2e B29-B33
- 【1518d56】补 embeddings×模型映射直接单测（共用 relay 出站/回写/计费三断言）
- 收尾回归：go vet ✓ 全部单测 ✓ e2e 60/60 ✓ vitest 15/15 ✓；测试残留清零（orgs/channels/models/users/api_keys/usage_logs 全 0）
- 累计：迭代 1-2 修 6 个 bug，迭代 3 修 1 族 3 个缺陷；完成承诺仍未满足（存在过 bug 且盲区未尽）
- 环境态：/tmp/tg-bin @1518d56 跑 :8081；mock :9110/:9111 存活；用户品牌 WIP（中转站→慧沐引擎）未动未提交

## 迭代 4 —— 2026-09-21 深夜
- 【修复 07d8f99 — bug #9】审计日志存量明文：54 行含明文密码/123 行真未脱敏（含真实厂商 key）→ 中间件 ScrubAuditHistory 启动幂等补洗 + router 装配 + 单测（洗 3 行/幂等 0/无关不动）；live 库启动实测洗净 123 行，复核 0 残留
- 【edb5cc6】补排队超时 TestQueueWaitTimeoutReturns502（并发闸门满 → 502 queue_timeout，挂账迭代 3 的缺口）
- 【修复 e92b462 — bug #10/#11】GET /api/org/alert-levels 对 alert_levels='' 的存量客户误报 404「客户不存在」→ 404 仅留给 row.ID==0（Raw+Scan 无行零值判别）；GORM Create 零值写空串压掉列默认 '[80]'（org/user 模型缺 default tag）→ 补 gorm:"default:'[80]'"，新库/存量库/Go 路径三态一致；alerts_test.go 3 用例（未配置 200+0、round-trip、列默认）
- 【修复 e64c141 — bug #12】客户管理员侧边栏待审批角标只在 onMounted 拉一次，审批/驳回后陈旧不刷新 → RequestListView 广播 quota-requests-changed，AdminLayout 监听重拉 + onUnmounted 清理；真机验证批准后角标即时归零
- 前端浏览器全量巡检（真机 :8081，Playwright）：平台全页 ✓；客户管理 7 页 ✓ 对账单三段勾稽精确；member 5 页 ✓（用量/额度/文档/密钥/模型）+ playground 全链路（流式回复+入100出50 footer+按授权过滤模型）；公开文档站/登录/注册页 ✓；UI 登录三角色闭环 ✓；各页控制台 0 错误（历史 401/403 均为换 token 测试余迹）
- 额度申请审批流端到端：member 提交 #18 → 侧栏角标 1 → org admin 批准 → 额度 1M→2M 到账 → quota_grants 流水 #187（operator=177）Σgrants==quota_limit 不变量成立 → member 侧状态已批准
- 收尾回归：go vet ✓ 全部单测 ✓ vitest 15/15 ✓；it4 测试数据清零（org 90/channel 598/model 1707/users 177,178/key 92/usage 2/grants 4/requests 2），真实渠道 1,2,3,7 未动
- 累计：12 个 bug 已修（迭代 1-2 六个、迭代 3 一族三个、迭代 4 四个）；完成承诺仍未满足——本轮仍发现并修复了新 bug
- 环境态：/tmp/tg-bin 最新（含 e64c141 前端）跑 :8081；mock :9110/:9111 存活；用户品牌 WIP 未动未提交（AdminLayout.vue 拆 hunk 提交）
- 迭代 5 候选盲区：/metrics 完整性与告警邮件链路（SMTP live）、月末归档/余额快照 goroutine 触发路径、Redis 协调器（需起 redis）、Caddy/多实例 HA 部署面、web 端 vitest 覆盖率扩面

---

## 迭代 5（Redis 协调器 + SMTP 邮件链路专项）

**发现并修复 1 个 bug（#13，已提交 00ba65d）：**
- `internal/coord/redis.go` cooldownLua 用 `TTL`（秒精度）做 keep-longer 比较：亚秒冷却 TTL=0 被 `cur<=0` 误判为"不存在"直接覆盖；秒级窗口边界也被截断（1500ms 剩余 → TTL=1 → 1200ms 新冷却误判"更长"反而缩短冷却）。改 `PTTL` 毫秒比较 + 回归测试（含秒级边界用例）。测试卫生一并修：闸门测试用唯一 scope 后缀 + 收尾释放，防 45s 租约残留污染下一轮。

**双实例 HA 环境全矩阵 PASS（:8084/:8085 + 共享 SQLite + Redis:6399 + 双 port mock + SMTP sink）：**
- X1 跨实例精确缓存（B 命中不打上游、不扣费）；X2 跨实例 429 冷却共享（B 秒拒）；X3 全局并发闸门（6 并发跨实例峰值=2、排队削峰）；X4 Redis 杀死 fail-open（coord_redis_errors 计数、请求不中断）；X5 计费不变量 Σusage_logs.cost == user.quota_used == org.quota_used（1420=1420=1420）
- Redis 重启自动恢复（缓存重新共享、响应一致）
- 渠道体检：连续 3 败自动禁用+邮件、端口恢复自动探测拉起+邮件（it5-ch-bad 死端口→ok9999 全程 live 验证）
- 场景 B 额度预警邮件：额度调至 2700、单笔跨 82.2% → 恰好 2 封（member+org_admin）、subject/body/水位全对；同档第二笔不发（边沿触发）；org 级未到线静默；alert_level=1 落库。中途确认三个"疑似 bug"实为设计：①被 precheck 拒的请求也写 0 费用计量日志（审计留痕，注释明示）；②充值后 alert_level 惰性复位靠下次结算（alerts.go:20 注释明示"不加拨备钩子"），第二周期能再次预警；③收缩额度至已耗尽不发信（每次请求已有显式 429，预警无增量信息）
- 场景 A 注册验证码：发码邮件送达、无 dev_code（SMTP 已配置）、60s 重发限频 429+retry_after、错码 400、跨实例注册（A 发码 B 注册，码存共享库）、新 org 初始额度 0、验证码用后即焚（重放 400）

**其他记录：**
- SQLite 双进程同时冷启动迁移有 DDL 窗口竞态（busy_timeout 对 DDL 无效）——探测确认正常跨进程写等待 2.177s 正常，多实例本来就是 PG 场景，不算产品 bug
- 测试环境已全部清理（:8084/:8085/:9112/:9113/:9114/:9115/:9999/:6399 已停，/tmp/tg-ha-it5 已删）；常驻 :8081 网关 + :9110/:9111 mock + 用户 :5173/:8083 未受影响
- bug #13 修复已过 go vet + 全量 go test，品牌 WIP 仍未提交（0 慧沐引擎字样混入）

**累计：迭代 1-5 共修复 14 个 bug（#13 本迭代）。下一迭代候选盲区：/metrics 完整性核对、月末归档/余额快照 goroutine 触发路径、vitest 前端覆盖、模型映射(model_mapping)改写链路边界。**

---

## 迭代 6（月度上限 + 成本中心 + 月末快照/归档 + RPM 专项）

**发现并修复 1 个 bug（#14，已提交 974f9f3）：**
- `internal/service/archive.go` 空表启动：`SELECT MIN(created_at)` 对空 usage_logs 返回 NULL，`Scan(&minTS)`（非指针 int64）直接报 converting NULL 错——代码本意空表返回 0 静默跳过（`if minTS == 0`），但 GORM 对 NULL 是 error 不是零值。后果：**启用归档的全新部署每次启动必现 ERROR 且自愈轮中断**；全部行归档完后也会复现。修复 `COALESCE(MIN(created_at),0)` + TestArchiveEmptyTable 回归（RED→GREEN）。live 复验：修复后空表启动 0 报错。

**全矩阵 live PASS（it6 环境 :8086 + mock :9116 + SQLite，retention=2 + per_key_rpm=6）：**
- M1 member 月度上限 400：2×200 放行、monthly_cost 精确累计、第 3 笔 429 monthly_limit_exceeded、被拒不加计数
- M2 惰性跨月：monthly_period 改 2026-08 → 放行且 monthly_cost 归零重记 200/2026-09（CASE WHEN 惰性重置 live 实证）
- M3 org 月度上限：org 级 429 monthly（company 文案）、member 放开后互不干扰
- CC 成本中心全链（此前从未测过的功能面）：建 2 中心、member 建 key 即指派、member/org 双路径改派、**历史锚定不可变**（改派前 6 笔未归集 None、改派后 2 笔锚新中心）、require_cost_center=1 裸建 key 400、报表按中心×模型×日聚合正确
- S1 月末快照：手动补跑端点（断链修复）200、时点值精确（=当前水位−9月消耗）、9 月账单链式勾稽 chain_ok=true（期末−期初==消耗）、同月重跑幂等（UPSERT 仍 1 行）、非法 period 400。注：启动自愈快照在 org 创建前执行则该 org 无 8 月快照（opening_missing 如实披露）——手动补跑端点正是为此设计，非 bug
- V1 厂商账单对账：vendor_cost 按模型 cost 价精确记账（1M/M → 每笔 100）、录入=我方差异 0、调高一倍 diff=-200 标红 50%、删除后 has_bill=false。注：UpdateModel 的 input/output_price 是 required（改 cost 价须整单重发）——API 契约非 bug
- A1 归档自愈 live：插 6 月×2 + 7 月×1 历史行 → 重启后 6 月导出 usage_logs-202606.jsonl.gz（2 行）+ 库内删除，7 月保留（retention=2 边界 Jul 1 精确）；再次重启幂等（0 导出 0 报错）
- R1 per-key RPM 令牌桶：rpm=6 burst 跟随 → 8 连发恰 6 过 2 拒（429 rate_limit_error）、11s 后回填恰 1 枚（1 过 1 拒）——容量与回填速率双精确

**累计：迭代 1-6 共修复 15 个 bug（#14 本迭代）。测试环境已清理；常驻 :8081 + :9110/:9111 未动。下一迭代候选：/metrics 指标完整性逐项核对、Caddy/部署面（deploy/ 配置与 SSE）、vitest 覆盖扩面、前端边界（超长输入/并发操作）。**

## 第 7 轮（it7）— /metrics 完整性 + 并发审批 + 登录双限流 + deploy 审查

**环境**：:8087 一次性环境（fresh DB），mock 上游 :9117-9121（正常/SSE、429、502、401、慢3s）；收尾已拆除。

**验证通过（live 52/52）**：
- 16 个指标族逐一触发断言：requests{200/403/404/429}、latency histogram（含 _count 精确数）、active_streams 中途≥1/结束归零、cache_hits 精确命中、queue_wait/queue_timeouts（并发闸门 1 过 1 超时）、upstream_429→key_cooldown、5xx×7→upstream_errors+熔断 channels_disabled、401→key_disabled、probe ok/fail、alert_triggers/alert_throttled、rpm 5过2拒、月度上限、余额耗尽（前3过第4拒）
- 并发审批竞态：10 线程同时 PUT → 恰好 1×200 + 9×404，quota_limit 恰好 +1000，流水恰好 +1；approve/reject 混发 10 路 → 恰好 1 胜者，拒绝路径零加额
- 登录双限流：用户名级 10/10（XFF 换 IP 绕开 IP 桶单独验证）、IP 级 5/5（桶回满后 5×401+7×429）、未知用户名走 dummy bcrypt 比对（防时序枚举）
- deploy/ 静态审查：Caddyfile flush_interval -1 ✓；docker-compose gateway2 depends_on gateway1 service_healthy 疑似缺 healthcheck —— 实测 Dockerfile 自带 HEALTHCHECK（wget /healthz），非 bug（用最小 compose 复现确认了无 healthcheck 时 compose 会硬失败，故核对过）；systemd LimitNOFILE/ProtectSystem ✓

**修复 3 个 bug（累计 17）**：
- **#15** metrics histogram `_count`/`+Inf` 曾对累积桶求和 → 虚高 10 倍（Prometheus rate 算 QPS 全错）。改为独立观察计数，加回归测试（含超 30s 最大桶的观察）。commit 4c6c3fe
- **#16** 预算告警 60s 节流把越限检查整个吞掉：突发跨阈值后流量静默（批量任务跑完即停——恰是最该告警的场景）会永久丢告警。改为节流时 LoadOrStore 去重挂起窗口到期兜底复查；单测注入定时器验证恰好挂 1 个 + 到期补发；live P5 轮询 60s+ 验证兜底路径真实触发。commit e578859
- **#17**（CRITICAL）backup.sh 朴素解析不剥行尾注释：example 原样 `path: "data/token_.db"  # 说明` → 注释残渣进文件名 → sqlite3 对不存在文件**静默新建空库**当备份（exit 0、4096 字节、无表），每晚 cron"备份完成"，恢复即全量数据丢失。修复：driver/dsn/path 三处剥离 ` #注释` + 尾空格裁剪 + 源库存在性守卫（缺失 exit 1）。三场景实测：带注释行备份出真数据、缺失库拒绝、带注释 driver 正确走 PG 分支。commit 0d458b2

**下一轮候选**：Go 单测覆盖盲区扫描（coord redis 分支、notifier）；tools/e2e run.py 与新行为对齐；usage_logs 归档月末边界；config.example.yaml 与代码逐项核对；前端 views 层手工探查。

## 第 8 轮（it8）— 体验密钥新功能 + org/member 平面全端点扫描

**插曲（用户需求）**：在线体验 API key 过期 → 管理员可自配的对外演示体验密钥。commit afdebef：
- GET/PUT `/api/platform/demo-key` + POST `/rotate`；专用客户「在线体验」+ online_demo（个人不限额，客户额度承担上限，走 AddOrgQuota 保 Σgrants）
- 模型授权全量替换；有效期 0=永久；轮换=吊销全部旧 key（即时 401）+ 签发新钥；明文存 settings（演示密钥刻意可反复查看）
- 前端 DemoKeyView + 菜单「在线体验」（AdminLayout hunk-split 提交，未带上品牌 WIP）；单测 5 例 + live 冒烟 17/17（/v1 全链路计费/停用过期 401/额度耗尽 429/越权 404）

**org/member 平面扫描（live 一次性环境 :8099 + mock :9110）——70 项全过、零 bug**：
- sweep1 45/45：子账号 CRUD 全生命周期；每一步 Σgrants==quota_limit（建号 1M→+500k→申请批 1.6M）；客户额度 20M→充值批 22M 流水一致；重复审批 404；授权模型替换/坏模型拒绝且不改库；强制归集下无中心建 key 400；member key 真实调 /v1 且成本归集到中心（org 视角带中心名）；额度申请批准/拒绝闭环；预警阈值 90/0；账单/对账单/CSV（BOM 刻意为之——Excel 中文坑）；org 停用 key 即时 401；tgp_ 访问令牌建/用/吊销/RBAC 边界（org 令牌进不了平台面）；自助注册（开发模式回验证码、错码 400）
- sweep2 25/25：转不限额（差值 -旧值 入流水 Σ归零）/转回限额（以当前消耗为起点）不变量保持；monthly_quota 负值 400、status 非法 400；删成员级联（key 行删+即时 401+JWT 失效）；客户间隔离（读/改/删一律 404 不泄露存在性）；org 统计/用量分页/成本交叉报表/银行信息/平台侧对账单+CSV+统计；send-code 限流 3/分（第 4 次 429）

**注意**：登录限流 5/分/IP 很容易在反复调试脚本时自伤——脚本应缓存 token（本轮踩过）。

**累计：17 bug 已修（迭代 1-7），迭代 8 零新 bug + 交付体验密钥功能。环境已清理。下一迭代候选：tools/e2e run.py 对齐核验、usage_logs 月度归档边界、config.example.yaml 逐项核对、coord redis 分支单测。**

## 第 8 轮续（it8+）— 四项候选全部收口，发现并修复第 18 个 bug

- **tools/e2e 对齐核验**：live :8081 实跑 `python3 tools/e2e/run.py` → **61/61 通过**（B 系列对账勾稽、C 系列不变量 Σgrants==quota_limit、quota_used==Σusage、缓存命中零计费、超扣上界、D1 残留清理）
- **config.example.yaml 逐项核对**：脚本提取全部键路径（含注释键）逐一 grep 代码引用——**零孤儿键**（生效键全有引用、注释键非死文档）
- **usage_logs 归档边界**：已有 5 测试（空表/导出删库/幂等/残缺重导/关闭）。交叉影响核实：对账单"已用"直接 SUM usage_logs，但链式勾稽 `期末used−期初used==期内消耗`（billing.go:263）会在账期被归档后自动 ChainOK=false 自我标记不平，不会静默给错数——安全。新增 3 个边界测试：月末最后一秒归档/保留窗首秒保留（半开区间精确性，PASS）、epoch 脏行
- **coord Redis 分支**：TestRedisCoord 本就是真实 Redis 集成测试但被 TG_TEST_REDIS_ADDR 门控从未在本地跑过——起一次性 redis-server :9123 实跑 → **PASS**（含 PTTL 毫秒比较的秒级窗口边界回归用例）

**修复 #18**：归档 `COALESCE(MIN(created_at), 0)` 把「表空」（NULL→0）与「存在 created_at=0 的 epoch 脏行」混为一谈——后者令归档**静默永久停摆**（每轮早退"无历史数据"，任何月份都不归档）。改 `sql.NullInt64` 按 Valid 判空 + 负时间戳早退。新边界测试先红后绿确认修复。commit fe759ba

**下一迭代候选**：前端 views 层手工探查（Playwright 走查管理台各页）、gateway SSE 断流/客户端断连结算路径、playground 模块剩余覆盖、多实例 PG 模式（docker-compose 起双网关验全局协调器 fail-open）。

## 第 8 轮续二（it8++）— 前端全角色 Playwright 走查，发现并修复 #19/#20/#21

环境：/tmp/tg-ui 一次性网关 :9091（root/rootpw123）+ vite :5174 + mock 上游 :9110；渠道 mock-ch(id5)、模型 mock-model（2M/8M）、客户「走查客户」20M、orgadm1、mem001（5M+mock-model）。

**修复 #19**（commit 906db9d）：SSE 流式客户端断开时 usage_logs 仍记 status=200（非流式记 499）——错误率统计把断连误算成成功。pipeSSE 错误块补 `ctx.Err() != nil → rec.Status=499`；红测 TestClientCancelDuringStreamLogged499 先失败后转绿（断言 499+no_usage=1+cost=0+不熔断）。

**修复 #20**（commit 604576c）：空库/零调用时 StatsOverview 的 by_org/by_user 带 omitempty（缺键）、by_model 为 null——前端看板裸读 `.length` 直接 TypeError 白屏（全新部署必现）。Overview 三字段去 omitempty + 预置 `[]GroupPoint{}`；TestStatsOverviewEmptyArraysContract 锁三 scope JSON 契约。排查插曲：pkill -f '/tmp/tg-ui/gw' 匹配不到（进程 cmdline 是 `./gw -config ...`），旧二进制一直服务——`ps -o pid,lstart` 对时间戳定位后kill 重启才见修复生效。

**修复 #21**（commit 23149ef）：JWT 过期后 axios 拦截器只清 localStorage，路由守卫读 Pinia store 内存 token——hash 推到 #/login 被守卫判「已登录访问 /login」弹回角色首页，过期用户困在持续 401 的破损页直到 F5。改为 401 时调 `useAuthStore().logout()` 双清（store 未初始化兜底）；Playwright 实证：换无效 token → 导航 → 干净落 #/login → 立即重登成功。

**三角色 UI 全页走查（21 页零 console 错误）**：
- platform 8 页：dashboard/orgs/org-detail/channels/models/usage/audit/demo-key + docs 站 + playground 对话（入100/出50 计费正确）
- org 8 页：dashboard（接入向导✓、勾稽表对无期初快照优雅降级、期内授权+20M与流水一致）/members（mem001 5M）/requests/keys/cost-centers（未配收款信息优雅兜底）/usage/recharges/billing
- member 5 页：models/keys/usage/quota/docs（文档带实时 key 前缀与授权模型）
- 越权导航：orgadm→/platform 弹回 org home ✓；mem→/platform 与 /org 均弹回 member home ✓

**UI 驱动的全链路闭环**：mem001 建 key（明文只显一次）→ curl /v1/chat 200（100/50）→ 我的用量扣减 600=100×2+50×8 ✓ → 发 1M 额度申请 → orgadm1 批准 → quota_limit 5M→6M、流水「额度申请 #1 审批通过」+1M、Σgrants==quota_limit ✓。充值申请 10M 入库（待确认+凭证回显）；空凭证提交被前端校验拦截。

**API 级 RBAC 矩阵（有效 token）**：member→org/mem 面 403/200；orgadm→platform 403、org 面 200；root→org 面 403（平台管理员只走平台面，与 UI 跳转口径一致）；无/假 token 401。三账号并发登录三 token 同时有效。登录限流 5/分/IP 两度自伤实证生效。

**累计：21 bug 已修。下一迭代候选：多实例 PG 双网关全局协调器 fail-open 实测、playground 剩余模块、前端表单极端输入 fuzz。**

## 第 9 轮（it9）— PostgreSQL 模式深测：e2e 全绿 + 迁移实测再抓 2 bug + 双网关全局协调器

环境：一次性 PG 17 集群 :5433（/tmp/tg-pg/data，用户 tg trust）、PG 网关 :8092（admin/admin123456）、源 SQLite 网关 :8094（migadm，aes_key 用 base64(32B)）、mock 上游 :9102（回显上游实收模型名）/ :9103（恒 429+计数）、隔离 redis :6399。

**PG 模式基线**：
- schema.sql 模板替换（AUTOINCREMENT→IDENTITY 等）实测建 19 表成功；启动日志确认 postgres DSN
- e2e 61/61 **全绿**（`TG_E2E_DB_DSN` 走 psql 只读断言；D 清理段的 db_exec 只支持 SQLite，/tmp 拷贝补 pg_exec 后 C/D 系列才可跑）——计费不变量、缓存零计费、超扣上界全部方言安全
- stats overview PG 方言 to_char 分桶正确（7 点数组契约）

**修复 #22**（commit 3f45cff）：postgres 模式启动日志打 `database.path`（sqlite 默认值 `data/token_.db`）——排障必被误导为单机部署。新增 `Database.LogDesc()`：postgres 打脱敏 DSN（password=***；非标准格式整体隐藏），4 用例单测锚定。

**修复 #23**（commit 3f45cff，真实搬迁实测抓到）：`-migrate-from-sqlite` 的 migrateTables 漏 `recharge_requests` 与 `audit_logs`——切 PG 搬数据**静默丢充值审批流水与操作审计**（源库 1+10 行实测不达）。补表入清单+单测锚定；重跑搬迁 53→64 行。排查插叙：渠道创建 API 的映射是 per-ability `upstream_model_name` 字段而非 `model_mapping`（我先传错字段名，非 bug）。

**迁移后全家桶 10/10 PASS**：三角色登录（bcrypt 原样达）、tgp_ 访问令牌跨库可用、迁移 API key /v1 200（渠道密文解密成功=同 aes_key 前提）、模型映射改写出站 mig-chat-upstream/回写对外名、充值单可见、**序列重置**（新建客户 id=2 无冲突；模型 id 跳号是 presets upsert 预耗序列，无害）、幂等重跑总行数=0、Σgrants==quota_limit（org 6,234,567==6,234,567、user 1M==1M）、双记账 quota_used==Σusage==2,400、缓存命中与 429 均零计费。

**双网关同 PG + Redis 全局协调器**（TG_REDIS_ADDR=127.0.0.1:6399，:8092/:8095）：
- N1 跨节点全局缓存：A 首打 miss、B 复打 `X-Tg-Cache: hit` ✓
- N2/N3 跨节点全局冷却：A 打恒 429 渠道触发冷却（上游计数+1），B 立即同模型请求**不打上游**（计数不变）、回 429 upstream_busy（全冷却语义有 scheduling_test 锚定，我预期 503 属猜错非 bug）✓
- N5 redis 出现 `tg:cd:ck:*`（冷却）与 `tg:cache:*`（缓存）键 ✓
- **fail-open**：shutdown redis 后双节点 /v1 继续 200（redis client 连接池报错仅入日志），计费继续准确（+3×600 与调用一一对应）

**累计：23 bug 已修（#22/#23 本轮）。下一迭代候选：playground 剩余模块深测、前端表单极端输入 fuzz、SSE 网关侧异常路径（上游断流/半包）。**
