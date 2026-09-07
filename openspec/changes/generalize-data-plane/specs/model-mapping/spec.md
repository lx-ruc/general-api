## ADDED Requirements

### Requirement: 渠道级模型映射配置
平台管理员 SHALL 能为每条渠道配置外部名→上游名的 JSON 映射（`channels.model_mapping`）；未配置或映射不含请求模型名时，上游名等于外部名（现行为不变）。

#### Scenario: 存量渠道零影响
- **WHEN** 渠道未配置映射（model_mapping 为空）
- **THEN** 请求以外部名原样转发，所有行为与升级前一致

#### Scenario: 配置映射后生效
- **WHEN** 渠道配置 `{"gpt-4o": "qwen-max-latest"}` 且用户请求 model=gpt-4o
- **THEN** 上游收到的请求体 model 字段为 qwen-max-latest，渠道 abilities 以 gpt-4o 匹配

### Requirement: 映射对计费/缓存/授权不可见
系统 SHALL 保证授权检查、缓存键、usage_logs 记录与价格快照全部使用外部名；映射仅改写出站请求体 model 字段与响应体（含 SSE 每块）中回显的模型名（改写回外部名）。

#### Scenario: 计费按外部名定价
- **WHEN** 请求外部名 gpt-4o 映射到上游 qwen-max-latest，gpt-4o 行定价 2,000,000 点/百万token
- **THEN** cost 按 gpt-4o 行价格计算，usage_logs.model_name=gpt-4o

#### Scenario: 客户看到的响应模型名为外部名
- **WHEN** 上游响应（含流式分块）回显 model=qwen-max-latest
- **THEN** 客户端收到的响应体 model 字段为 gpt-4o

### Requirement: 渠道测试应用映射
渠道测试（TestChannel）SHALL 应用该渠道的模型映射后发起上游请求，使配置错误（映射到不存在的上游模型名）能在测试中暴露。

#### Scenario: 映射写错上游名
- **WHEN** 渠道映射指向上游不存在的模型名并点击测试
- **THEN** 测试返回上游错误（如 404/400），而非误报成功
