# 设计：成本中心

## Context

`usage_logs` 已记 `api_key_id`（真相源），缺归集维度。客户是财务，报表必须财务级干净。`apiauth.go` 的联表 SELECT 已加载 key/user/org，加一列零成本。

## Goals / Non-Goals

**Goals:** 受控词表 + 结算快照归集 + org/platform 两级报表 + 未归集披露
**Non-Goals:** 部门树/二维（部门×项目）——v1 单维，命名约定扛；内容级审计（另案慎议）

## Decisions

### D1：受控词表，否决自由文本
自由文本 = "AI客服/ai客服/智能客服"孤儿桶 + 改名不可能 + 无生命周期。AWS cost allocation tags / 阿里云费用分账先例全是受控维度。维护者挂 org_admin（本就在做管理动作）。
```sql
cost_centers(id, org_id, name, status, created_at, updated_at, UNIQUE(org_id,name))
-- status: 1启用/0归档；永不硬删（历史引用）；归档后下拉消失、报表标"（已归档）"
```

### D2：单维 v1
无部门实体（org→users 平铺），建树是 org 模型大改。"谁花的"已由 user_id 覆盖，成本中心补"为哪个项目花的"。

### D3：结算时快照，否决实时 JOIN
`usage_logs.cost_center_id` 在 Settle 时落库。改 key 归属只影响未来——历史账单不可变（财务预期），与价格快照同哲学：**维度属性取事件时刻值**。SCD 时间区间表是数Warehouse 标准答案，但对账单场景过度设计。

### D4：写入链路（零额外查询）
```
apiauth.go 联表 SELECT + k.cost_center_id → KeyInfo 加字段 → context
handler 构造 rec（UsageLog）时 rec.CostCenterID = ki.CostCenterID → 随既有 INSERT 落库
```
applyUsage 不动。加列两处铁律（schema.sql + migrate.go）。

### D5："未归集"恒置底 + 占比警示
报表自带纠偏：财务看到未归集占比会去催 tagging 纪律，比文档有效。中心列表页显示各中心本月消耗与 key 数。

### D6：权限矩阵
员工：建自己 key 时选中心（下拉仅启用项）、改自己 key 归属。org_admin：中心 CRUD、改派任何人、require 开关、报表（售价口径）。platform：跨 org 报表（+毛利列），不介入各家词表。

### D7：require_cost_center 开关（org 级，默认关）
存 orgs 新列或 org settings；默认关保证存量客户零感知。科技公司自助、传统企业强制——纪律度公司自定。

## Risks / Trade-offs

- [词表冷启动摩擦（没建中心建不了 key）] → 开关默认关；报表未归集桶倒逼
- [员工乱选中心] → org_admin 可改派；报表按中心聚合天然暴露错配
- [usage_logs 大表加列] → 可空 INTEGER，SQLite/PG 均廉价

## Migration Plan

1. 加表加列（幂等）；2. 部署后 org 按需建词表、开开关；3. 回滚：二进制回退，残留列/表无害。
