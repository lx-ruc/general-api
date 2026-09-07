## ADDED Requirements

### Requirement: 成本中心词表管理
org 管理员 SHALL 能在本 org 内创建/重命名/归档成本中心；名称 org 内唯一；归档（status=0）后不出现在新建 key 的下拉中但历史数据保留，且系统 MUST NOT 提供硬删除。

#### Scenario: 创建与重名
- **WHEN** org 管理员创建中心"AI客服"，再创建同名中心
- **THEN** 第一次成功；第二次被拒（org 内唯一约束）

#### Scenario: 归档语义
- **WHEN** 已有 key 归集的中心被归档
- **THEN** 新建 key 下拉不再出现该中心；历史报表继续显示其数据并标注"（已归档）"

### Requirement: key 归集与改派
员工创建 key 时 SHALL 可从本 org 启用中的中心下拉选择归属（可空=未归集）；员工可改自己 key 的归属，org 管理员可改派本 org 任何 key；归属变更 MUST NOT 影响历史 usage_logs（快照不变）。

#### Scenario: 强制归集
- **WHEN** org 开启 require_cost_center 后员工创建 key 未选中心
- **THEN** 创建被拒并提示需选择成本中心

#### Scenario: 改派不动历史
- **WHEN** key 从中心A改派到中心B后查看上月报表
- **THEN** 上月消耗仍归中心A，本月起归中心B

### Requirement: org 隔离
成本中心的一切查询与操作 MUST 强制 `WHERE org_id`，跨 org 不可见。

#### Scenario: 越权访问
- **WHEN** org1 管理员以 org2 的中心 id 请求改名
- **THEN** 返回未找到
