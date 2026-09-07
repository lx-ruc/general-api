## 1. 数据层与写入链路

- [x] 1.1 schema.sql：新表 cost_centers（UNIQUE(org_id,name)）+ api_keys.cost_center_id + usage_logs.cost_center_id；migrate.go alters 同步；model 实体（CostCenter、APIKey/UsageLog 加字段）；另补 orgs.require_cost_center、migrate_sqlite.go 表清单加 cost_centers
- [x] 1.2 apiauth.go 联表 SELECT 加 k.cost_center_id → KeyInfo 加字段进 context
- [x] 1.3 handler.go 构造 UsageLog rec 时赋值 CostCenterID 快照（入口处一次赋值，含缓存命中路径）
- [x] 1.4 orgs 加 require_cost_center 列（schema.sql + migrate.go 两处铁律）

## 2. org 端

- [x] 2.1 中心 CRUD 端点（建/改名/归档，强制 org_id；无删除）+ 路由注册（internal/api/org/costcenter.go）
- [x] 2.2 key 归集：CreateKey 校验中心属本 org 且启用；改派端点（员工自己 / org 管理员任何人）；require 开关生效
- [x] 2.3 org 报表端点：中心 × 模型 × 日聚合 + 未归集置底与占比（SQLite/PG 双方言按日分桶）
- [x] 2.4 前端：成本中心管理页（列表含本月消耗/key 数/归档、require 开关、报表卡片）、key 归集改派（密钥一览内联下拉）、报表页（筛选）

## 3. member / platform 端

- [x] 3.1 member CreateKey 加 cost_center_id；MyKeysView 下拉 + 归属列（含自助改派）
- [x] 3.2 platform 跨 org 交叉报表端点（org × 中心，含毛利列）+ 前端页（CostCenterCrossView，org 列合并单元格）

## 4. 测试与收尾

- [x] 4.1 测试：快照不变性（改派后历史不动，gateway 全链路）、org 隔离越权、未归集桶、require 拒绝、org 报表无毛利字段断言
- [x] 4.2 `go test ./internal/...` 全绿；`go vet ./...`；`make build` 通过
- [x] 4.3 README/docs 增补成本中心一节（功能总览 + 计费模型补审计不变量）
