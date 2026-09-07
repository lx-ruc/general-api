# 设计：数据面泛化

## Context

`internal/gateway/handler.go` 的 `ChatCompletions` 是一条写死 chat 语义的编排：限流 → 授权/预检 → 缓存 → `SelectCandidates` → tryCandidate 循环（闸门/转发/响应分类）→ defer 结算。embeddings 与模型映射都要复用这条链路。约束：

- 计费不变量（全整数、Precheck advisory、Settle 无条件双记账）不可动摇
- SSE 红线：不设 WriteTimeout / 上游整体 Timeout
- chat 现有行为（含 20 个编排测试）必须零回归

## Goals / Non-Goals

**Goals:**
- 共享中继管线：端点差异收敛为一个 `RelaySpec`（缓存白名单字段、usage 解析函数、是否流式、stream_options 注入开关）
- `/v1/embeddings` 全链路：鉴权/限流/授权/额度/Key 池/冷却/闸门/缓存/结算
- 渠道级模型映射：外部名 → 上游名，对计费/缓存/授权不可见

**Non-Goals:**
- images/audio/completions 端点（另立 change，需先做计费泛化）
- Claude/Gemini 原生协议适配器（官方 OpenAI 兼容端点已覆盖主流场景，预设渠道即可）
- 跨模型降级（已否决：绕过授权白名单）

## Decisions

### D1：管线抽取为 RelaySpec 参数化，而非复制 handler

`ChatCompletions` 与新的 `Embeddings` 共用一个内部 `relay(w, r, spec)`；spec 携带：

| 差异点 | chat | embeddings |
|---|---|---|
| 上游路径 | 渠道 path | 渠道 path（厂商兼容层同为 `/v1/embeddings`，路径可配） |
| stream_options 注入 | 是 | 否（embeddings 无流式） |
| 缓存白名单 | model/messages/temperature/... | model/input/encoding_format/dimensions |
| usage 解析 | prompt+completion | 仅 prompt_tokens（completion 恒 0） |

**备选**：复制一份 handler 改 100 行——被否决，重试/冷却/闸门逻辑会双份漂移。

### D2：embeddings 计费复用 CalcCost，零 schema 改动

`CalcCost = ceil((pt×ip + ct×op)/1M)`，embeddings 的 usage 无 completion，ct=0 时公式自动退化为 `ceil(pt×ip/1M)`。`models` 表加一行（`input_price` = 每百万 token 价，`output_price` 置 0）即完成定价；授权走现成 `user_model_grants`；`cost_input_price` 毛利核算同理。**不新增计费代码路径**。

### D3：模型映射锚定外部名，三处不可见

- 缓存 key：选渠道前已计算，天然用外部名
- `usage_logs.model_name`：记外部名，价格快照查外部名行
- 授权检查：外部名 ∈ grants（映射不产生任何绕过面——这是与已否决的"跨模型降级"的本质区别：映射是管理员静态同义名，不是运行时降级）

改写点两处：出站请求体 `model` 字段（选渠道后）；响应体 `"model":"上游名"` 改写回外部名（非流式 JSON 感知改写；流式用 `"model":"<上游名>"` 完整字面量替换——只命中 model 字段模式，content 中同名裸文本不受影响）。

**备选**：双向映射（one-api 做了反向）——否决，我们只需外部→上游单方向，反向徒增配置歧义。

### D4：映射存 `channels.model_mapping`，JSON 对象

`{"外部名": "上游名", ...}`，空串/`{}` = 无映射（现网渠道零影响）。加列走两处铁律：`schema.sql` + `migrate.go` alters。

### D5：映射经 Candidate 携带

`SelectCandidates` 已一次性取渠道行，`Candidate` 结构加 `ModelMapping map[string]string`，tryCandidate 构造出站 body 时应用。避免 handler 再查一次库。

## Risks / Trade-offs

- [管线抽取引入 chat 回归] → 以现有 20 个测试为门禁，抽取前后全绿才算完成；`TestExactCacheHit`/`Test429CooldownThenFallback` 等覆盖了最脆弱路径
- [流式 model 改写的字面量替换误伤] → 只替换 `"model":"<name>"` 带引号完整形态；content 中出现该形态的概率可忽略且语义无害
- [embeddings 上游不回 usage] → 沿用 NoUsage 标记不计量（宁少收不乱收），与 chat 一致
- [映射配置写错上游名导致上游 404] → `TestChannel` 应用同样映射，测试按钮即暴露配置错误

## Migration Plan

1. 加列 `model_mapping`（幂等 alter，存量库无感，默认空 = 行为不变）
2. 部署后：管理台为需要的渠道配置映射；`models` 表添加 embedding 模型并定价、授权
3. 回滚：二进制回退即可，映射列残留无害（旧代码不读）

## Open Questions

- （已决）embedding 模型缓存是否默认开：沿用全局 `cache_ttl`，同语义（相同输入向量可复用）
- 渠道测试按钮对 embedding-only 渠道（无 chat ability）如何选模型：取第一条 ability 即可，不区分端点
