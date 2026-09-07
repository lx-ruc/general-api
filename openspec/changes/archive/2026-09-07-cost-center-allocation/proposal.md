# 成本中心：项目/部门级用量归集

## Why

企业客户的财务第一问是"每个团队/项目花了多少"。我们已有 `usage_logs.api_key_id`（真相源地基）但没有任何归集维度，只能答"每个员工花了多少"。one-api 因扁平用户模型结构上做不到部门归集——这是超越线四件套的第一件，也是杀手级的一件。

## What Changes

- 新表 `cost_centers`（org 级受控词表：公司管理员维护项目/部门清单，支持归档、永不硬删）
- `api_keys.cost_center_id`（建 key 时从下拉选择，可空=未归集）+ `usage_logs.cost_center_id`（**结算时快照**，历史不随改归属漂移）
- org 级开关 `require_cost_center`（默认关，存量零感知）：开启后建 key 必须归集
- org 管理台新页：成本中心报表（中心 × 模型 × 日聚合，**"未归集"恒置底并显示占比**）；key 归集/改派管理
- 员工端：建 key 下拉（仅启用中的中心）、我的密钥显示归属
- 平台端：跨 org（org × 中心）毛利交叉报表（`cost − vendor_cost`）

不改：计费路径（快照在 applyUsage 构造 rec 时顺带落库）、缓存/闸门/冷却行为、`service/quota.go`。

## Capabilities

### New Capabilities
- `cost-centers`: 词表 CRUD、归档语义、key 归集与改派规则、强制归集开关
- `cost-reporting`: 结算快照口径、org/platform 两视角报表、未归集披露与毛利隔离

### Modified Capabilities
<!-- 无存量 spec -->

## Impact

- `internal/database/schema.sql` + `migrate.go`：1 新表 + 2 加列（两处铁律）
- `internal/middleware/apiauth.go`：联表 SELECT 加 `k.cost_center_id`（零额外查询，随 KeyInfo 进 context）
- `internal/gateway/handler.go`：构造 UsageLog rec 时赋值快照
- `internal/api/org/`：中心 CRUD、key 改派、require 开关、报表端点
- `internal/api/member/`：CreateKey 加参数；`internal/api/platform/`：毛利交叉报表
- `web/src/views/org/`（新报表页 + key 归集）、`member/MyKeysView.vue`、`platform/`
- 测试：快照不变性（改归属后历史报表不动）、org 隔离、未归集桶
