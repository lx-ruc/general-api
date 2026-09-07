## ADDED Requirements

### Requirement: 提供 /v1/embeddings 端点
系统 SHALL 在 `/v1/embeddings` 提供 OpenAI 兼容的 embeddings 转发端点，复用与 chat 相同的 API key 鉴权、per-key 限流、模型授权、额度预检/结算、渠道选择（优先级+权重+Key 池）、429 冷却、并发闸门与精确缓存机制。

#### Scenario: 已授权用户成功调用
- **WHEN** 持有 `model` 为 embedding 模型授权的用户以有效 API key POST `/v1/embeddings`
- **THEN** 网关按既有渠道选择规则转发上游并透传响应，响应头携带 `X-Tg-Channel-Id`

#### Scenario: 未授权模型被拒绝
- **WHEN** 用户请求的 embedding 模型不在其 `user_model_grants` 内
- **THEN** 返回 `403 model_not_allowed`，不产生上游请求与扣费

### Requirement: embeddings 按输入 token 计费
系统 SHALL 按 `ceil(prompt_tokens × input_price / 1M)` 计算 embedding 调用成本（completion 恒为 0），与 chat 共用同一计费与结算路径（双层额度同步扣减、usage_logs 记录价格快照与厂商成本）。

#### Scenario: 正常计量
- **WHEN** 上游返回 usage.prompt_tokens=100，模型 input_price=1,000,000（点/百万token）
- **THEN** cost=100，员工与公司 quota_used 同事务各 +100

#### Scenario: 上游未返回 usage
- **WHEN** 上游 2xx 但无 usage 字段
- **THEN** 记录 no_usage 不计量（cost=0），行为与 chat 一致

### Requirement: embeddings 支持精确缓存
系统 SHALL 对非流式 2xx 的 embeddings 响应启用精确缓存，缓存键由 model/input/encoding_format/dimensions 白名单字段按固定顺序构成；命中时回放相同 body、`X-Tg-Cache: hit`、cost=0。

#### Scenario: 相同输入命中缓存
- **WHEN** 同一 API key 在 cache_ttl 内发送完全相同的 embeddings 请求两次
- **THEN** 第二次上游零请求、body 与首次逐字节相同、usage_logs 记 cost=0 且 cache_hit=1

#### Scenario: input 数组顺序不同不命中
- **WHEN** 两次请求的 input 数组元素相同但顺序不同
- **THEN** 缓存不命中，正常转发上游
