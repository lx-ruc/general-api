# 数据面泛化：共享中继管线 + /v1/embeddings + 渠道模型映射

## Why

对标 one-api 的差距分析（2026-09-07）确认两个数据面硬缺口：一是只有 `/v1/chat/completions`，RAG/知识库场景的客户（企业落地最常见形态）需要 `/v1/embeddings`，现在只能绕开我们；二是对外模型名与上游模型名硬绑定，换渠道/灰度/别名都要客户改代码，模型名无法解耦。两者共用同一条编排链路（限流→授权→预检→缓存→选渠道→闸门→转发→分类→结算），应先抽出共享管线再叠加能力，避免 handler 复制膨胀。

## What Changes

- 从 `ChatCompletions` 抽出共享中继管线：转发/重试/冷却/闸门/结算骨架不动，把端点差异（缓存白名单、usage 解析、stream 处理）收敛为可参数化的 `RelaySpec`；chat 行为零变化
- 新增 `/v1/embeddings` 端点（API key 鉴权 + per-key 限流 + 模型授权 + 额度预检/结算 + Key 池/冷却/闸门/精确缓存全部继承）；计费复用 `CalcCost`（completion=0 时自动退化为 `ceil(pt×input_price/1M)`，纯输入计费）
- `channels` 新增 `model_mapping` 列（JSON：外部名→上游名）；选渠道后改写出站请求体 `model` 字段，响应体（含 SSE 每块）改写回外部名；计费/缓存/授权全部锚定外部名，映射对客户完全不可见
- `TestChannel` 与后续探活逻辑同样应用映射
- 前端：渠道表单增加模型映射编辑；渠道测试结果展示映射生效情况

不改：计费公式与 quota 语义、SSE 透传红线（不设 WriteTimeout/整体 Timeout）、缓存键结构（仅白名单按端点变化）、熔断/协调器行为。

## Capabilities

### New Capabilities
- `embeddings-relay`: `/v1/embeddings` 端点的转发、授权、计费与缓存行为
- `model-mapping`: 渠道级外部名→上游名映射的配置、生效范围与不可见性约束

### Modified Capabilities
<!-- openspec/specs/ 当前为空，无存量 capability 需要修改 -->

## Impact

- `internal/gateway/handler.go`：管线抽取 + Embeddings handler（最大改动面，需保证 chat 回归零变化）
- `internal/api/router.go`：注册 `/v1/embeddings`
- `internal/database/schema.sql` + `internal/database/migrate.go`：`channels.model_mapping` 加列（两处铁律）
- `internal/gateway/selector.go`：候选结构携带映射（或 handler 侧查询）
- `internal/api/platform/handler.go`：CreateChannel/UpdateChannel 接收映射、TestChannel 应用映射
- `internal/model/channel.go`：ModelMapping 字段
- `web/src/views/platform/ChannelListView.vue` + `web/src/api/platform.ts`：映射编辑 UI
- 测试：`internal/gateway/handler_test.go` 扩展（embeddings 计费/缓存；映射改写与不改写计费）
- 无新增依赖；`service/quota.go`、`coord/*` 零改动
