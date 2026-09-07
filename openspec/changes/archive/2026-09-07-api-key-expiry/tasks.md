## 1. 入口与展示

- [x] 1.1 member CreateKey 加可选 expires_at（>now 校验，400 语义）；响应回显过期时刻
- [x] 1.2 MyKeysView 创建对话框加选择器（快捷 7/30/90 天/永久）；列表过期徽标（已过期红/≤7天橙）
- [x] 1.3 member.ts 类型同步

## 2. 测试与收尾

- [x] 2.1 测试：过去时刻 400；设定后过期调用 401（既有中间件路径回归）
- [x] 2.2 `go test ./internal/...` 全绿；`go vet ./...`；`make build` 通过
