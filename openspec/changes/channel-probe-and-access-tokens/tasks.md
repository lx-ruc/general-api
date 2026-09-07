## 1. 探活核心复用与调度

- [ ] 1.1 从 TestChannel 抽出"渠道+模型发请求并判定"的复用核心（含模型映射若 generalize-data-plane 已合入）
- [ ] 1.2 config.go + config.example.yaml：`gateway.channel_test_interval`（默认 0=关）、`channel_probe_fail_threshold`（默认 3）
- [ ] 1.3 main.go 探活 goroutine：周期遍历启用渠道 → 第一条 ability 模型探活 → 内存连续失败计数 → 达阈值 status=0+备注；优雅退出
- [ ] 1.4 metrics.go：`tg_channel_probe_result_total`（result=ok/fail）；探活失败日志（配置 smtp 时邮件告警）
- [ ] 1.5 测试：连续失败达阈值禁用+备注；中途成功清零；interval=0 不启动

## 2. 访问令牌数据层与鉴权

- [ ] 2.1 schema.sql 新表 `access_tokens`（token_hash 唯一索引）+ migrate.go；model 实体
- [ ] 2.2 令牌生成：`tgp_` + 32 字节随机，SHA-256 落库（复用 auth.HashAPIKey）
- [ ] 2.3 JWTAuth 中间件双轨：`tgp_` 前缀查表载入属主 → 同一 context 注入；过期/吊销 401；last_used_at 节流（>60s）
- [ ] 2.4 测试：令牌过 RBAC 与网页登录等价（公司管理员令牌 org 隔离生效）；吊销/过期即 401；节流不写放大

## 3. 令牌管理接口与前端

- [ ] 3.1 令牌 CRUD 端点（创建/列表仅本人/吊销），挂在平台与 org 两侧 profile 路由
- [ ] 3.2 web/src/api 同步类型；个人设置页"访问令牌"区块（创建弹窗显示一次明文+复制、列表、吊销）
- [ ] 3.3 渠道列表展示探活禁用备注（现有备注列复用，确认透出）

## 4. 收尾

- [ ] 4.1 config.example.yaml 注释与 README/docs（探活配置、令牌使用与安全说明：泄漏=属主全权限）
- [ ] 4.2 `go test ./internal/...` 全绿；`go vet ./...`；`make build`（含前端）通过
