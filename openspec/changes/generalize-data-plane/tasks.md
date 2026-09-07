## 1. 共享中继管线抽取（chat 零回归为门禁）

- [ ] 1.1 定义 RelaySpec（缓存白名单、usage 解析、流式开关、stream_options 注入开关），把 ChatCompletions 主体抽为内部 relay(w, r, spec)
- [ ] 1.2 ChatCompletions 改为薄壳调用 relay；`go test ./internal/gateway/...` 现有 20 用例全绿
- [ ] 1.3 `go vet ./...` 与 `go build ./...` 通过

## 2. /v1/embeddings 端点

- [ ] 2.1 handler.go 新增 Embeddings 方法：RelaySpec 定制（无流式、缓存白名单 model/input/encoding_format/dimensions、usage 仅 prompt_tokens）
- [ ] 2.2 router.go 注册 `v1.POST("/embeddings", h.Embeddings)`
- [ ] 2.3 测试：授权拒绝 403；正常调用计量（ct=0 退化公式）；缓存命中 cost=0/cache_hit=1/body 逐字节相同；input 数组顺序不同不命中；Key 池与 429 冷却对 embeddings 同样生效（任选一条路径回归）

## 3. 渠道模型映射（后端）

- [ ] 3.1 schema.sql 加 `channels.model_mapping TEXT NOT NULL DEFAULT ''`；migrate.go alters 同步加列；model/channel.go 加字段
- [ ] 3.2 selector.go：Candidate 携带 ModelMapping（SELECT 带出该列，json.Unmarshal 容错空串）
- [ ] 3.3 handler.go tryCandidate：出站 body.model 应用映射；响应改写回外部名（非流式 JSON 感知；流式 `"model":"<上游名>"` 字面量替换）
- [ ] 3.4 platform/handler.go：CreateChannel/UpdateChannel 接收并校验 model_mapping（JSON 对象，值非空）；TestChannel 应用映射
- [ ] 3.5 测试：无映射渠道行为不变；映射后上游收到上游名、客户收到外部名、计费/usage_logs 锚外部名；TestChannel 对错误映射返回失败

## 4. 前端与收尾

- [ ] 4.1 ChannelListView.vue 渠道表单加模型映射编辑（JSON textarea + 格式校验提示）；platform.ts 类型同步
- [ ] 4.2 新增 embedding 模型走现有定价表单验证 input_price-only 语义（output 0 显示合理）
- [ ] 4.3 README 功能总览与 docs 增补 embeddings/模型映射两节
- [ ] 4.4 `make build`（含前端）通过；`go test ./internal/...` 全绿
