# vendor-reconciliation Specification

## Purpose
TBD - created by archiving change billing-statements. Update Purpose after archive.
## Requirements
### Requirement: 厂商账单录入
平台管理员 SHALL 能按（账期 × 渠道）录入厂商实际计费金额与备注，同键重复录入以最新为准。

#### Scenario: 录入月度账单
- **WHEN** 平台管理员录入 2026-08 渠道 3 的厂商账单 1,234,567 点
- **THEN** 差异报表立即可用该值参与比对

### Requirement: 差异报表
系统 SHALL 生成"我方 vendor_cost 聚合 vs 录入厂商账单"的差异报表，偏差率超过 2% 的行 MUST 标红，并支持查看该渠道账期内的用量构成（含 no_usage 笔数与其对应漏损说明）。

#### Scenario: 超阈值标红
- **WHEN** 某渠道我方聚合 1,000,000 点、厂商账单 1,050,000 点（偏差 5%）
- **THEN** 报表该行标红并展示差异 50,000 点

#### Scenario: no_usage 漏损可见
- **WHEN** 差异行下钻
- **THEN** 显示该渠道账期内 no_usage 请求笔数，说明其为我方零计量但厂商实际计费的漏损来源

