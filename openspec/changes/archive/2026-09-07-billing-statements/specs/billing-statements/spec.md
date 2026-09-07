## ADDED Requirements

### Requirement: 月度对账单生成
系统 SHALL 为任意 org 生成月度对账单，含：口径三声明头（归期=结算时刻、时区、点数汇率）、额度勾稽段、冲减明细段、用量明细段（模型 × 成本中心 × 日，缓存命中单列，未归集桶适用成本中心规格）。

#### Scenario: 跨月流式归期
- **WHEN** 请求 8 月 31 日 23:50 发起、9 月 1 日 00:10 结算完成
- **THEN** 该笔消耗计入 9 月账单

#### Scenario: 历史月份无期初
- **WHEN** 生成快照功能上线前月份的账单
- **THEN** 勾稽段期初显示"—"，消耗与明细段正常

### Requirement: 链式勾稽
勾稽段 SHALL 满足：期初可用 + 本期追加(grants>0) − 本期冲减(grants<0) − 本期消耗(costΣ) = 期末可用，且下一期期初等于本期期末；链条断裂时 MUST 显式标记校验失败而非静默。

#### Scenario: 勾稽自洽
- **WHEN** org 9 月有快照期初 100 万点、追加 50 万、冲减 10 万、消耗 80 万
- **THEN** 期末显示 60 万，与 10 月期初一致，校验标记 ✓

### Requirement: 冲账机制
平台管理员 SHALL 能通过负数额度追加执行事故冲减；冲减 MUST 同步减少额度并记录审计流水；账单冲减段单独列示账期内全部负数追加。

#### Scenario: 事故冲减入账
- **WHEN** 平台管理员对 org 追加 −100,000 点并填事由
- **THEN** org 可用额度减少 100,000 点；quota_grants 出现负值流水；当月账单冲减段显示该笔及事由

### Requirement: 视角隔离
org 视角对账单 MUST NOT 包含 vendor_cost、成本价或毛利字段；平台视角 SHALL 额外包含厂商成本与毛利列。

#### Scenario: org 导出无毛利
- **WHEN** org 管理员导出本 org 账单 CSV
- **THEN** 文件不含任何成本价/厂商成本/毛利列

### Requirement: CSV 导出格式
CSV MUST 为 UTF-8 带 BOM 编码、末行含汇总行、文件名形如 `对账单_{org名}_{YYYY-MM}.csv`。

#### Scenario: Excel 兼容
- **WHEN** 财务用 Excel 打开导出的 CSV
- **THEN** 中文正常显示（BOM 存在），末行可对账总数
