## ADDED Requirements

### Requirement: 结算快照归集
系统 SHALL 在每次结算时把 key 当时的 cost_center_id 快照写入 usage_logs；该快照是报表归集的唯一口径。

#### Scenario: 快照口径
- **WHEN** 请求结算时 key 属中心A，结算后 key 改派中心B
- **THEN** 该笔消耗在报表中归中心A

### Requirement: org 成本报表
org 管理员 SHALL 能按时间范围/中心/模型筛选，获得中心 × 模型 × 日的聚合报表（请求数含缓存命中单列、tokens、费用），其中"未归集"MUST 恒置底显示并标注占比。

#### Scenario: 未归集披露
- **WHEN** org 存在未归集 key 的消耗
- **THEN** 报表出现"未归集"行，位于所有具名中心之后，并显示其占比

#### Scenario: org 视角无毛利
- **WHEN** org 管理员查看成本报表或导出
- **THEN** 任何字段不含 vendor_cost、cost_*_price 或毛利信息

### Requirement: 平台毛利交叉报表
平台管理员 SHALL 能查看跨 org 的（org × 中心）交叉报表，含毛利列（cost − vendor_cost）。

#### Scenario: 平台视角
- **WHEN** 平台管理员查看交叉报表
- **THEN** 可见各 org 各中心的消耗、厂商成本与毛利，同名中心按 org 区分
