# 多 Key 轮询压测指南

测「多 key 池能扛多大并发」不要直接拿真实厂商 key 冲——烧钱、各家 RPM/TPM 口径不一、
数字不可复现。正确姿势：**用可配 RPM 的 mock 上游模拟厂商限速，压本地网关**，
从 `/metrics` 看调度行为是否按设计工作。

## 容量的正确心智模型

- **吞吐上限 = Σ(每把 key 的 RPM) / 60**。3 把 key、单 key 120 RPM → 池容量 6 rps；
  扩到 6 把 → 12 rps，线性扩展，与网关自身无关。
- **并发数 ≠ 吞吐**。并发是同时在途的请求数（SSE 长连接会推高并发但不推高 rps）；
  容量瓶颈永远是上游配额，网关只负责把配额吃满、把超载快速拒绝。
- 网关在超载时的正确行为：goodput 锁死在池容量、多余请求**毫秒级**回 429
  `upstream_busy`（key 冷却后选择器直接跳过，不打无效上游请求）、冷却中的 key 到点自动回流。

## 工具

```bash
# mock 上游：OpenAI 兼容，按 Authorization 分 key 计 RPM，超限回 429 + Retry-After
go run ./tools/mockupstream -addr :9100 -rpm 120 -latency 300 -jitter 150
#   -rpm 0 = 不限速（测网关自身吞吐上限时用）

# 压测器：固定 rps + 并发 worker，统计状态码分布与延迟分位
go run ./tools/loadgen -c 16 -rps 15 -d 60s -key sk-xxx -model mock-chat
#   -rps 0 = 全速打（测并发上限）；-stream 切 SSE
```

## 步骤

1. **本地配置改两项**（`config.yaml`，压测完改回）：
   - `gateway.per_key_rpm: 60 → 100000`（否则客户端侧限流 1 rps 先掐死流量）
   - `gateway.cache_ttl: 5m → 0s`（否则重复请求命中缓存，根本不打上游）
2. 建**单价为 0** 的模型（压测不扣额度）+ 渠道指到 mock，Key 池每行一把：

   ```json
   POST /api/platform/channels
   { "name": "压测-mock", "base_url": "http://127.0.0.1:9100",
     "upstream_key": "mk-aaa\nmk-bbb\nmk-ccc", "priority": 10,
     "models": [{ "model_name": "mock-chat" }] }
   ```

3. 给测试子账号授权该模型，签发 API key，先单发一次确认 200。
4. **阶梯加压**：半载 → 满载 → 超载，每档 60s，档间看 `/metrics` 增量：
   - `tg_gateway_upstream_429_total` — 上游 429 次数（触发换 key 重试）
   - `tg_gateway_key_cooldown_total` — key 进冷却次数
   - `tg_gateway_queue_wait_seconds` / `queue_timeouts_total` — 配了 `channel_max_concurrency` 才有排队
5. **扩池复测**：key 数翻倍、同样压力，goodput 应同步翻倍（验证线性扩展）。

## 实测数据（2026-09-09，本地 :8081，mock 延迟 300±150ms）

| 档位 | key 数 | 目标 rps | 理论容量 | HTTP 200 | HTTP 429 | 备注 |
|------|--------|----------|----------|----------|----------|------|
| 半载 | 3 | 3 | 6 rps | 100%（3.0 rps） | 0 | key 均匀分摊 56/48/62 |
| 满载 | 3 | 6 | 6 rps | 100%（6.0 rps） | 0 | 内部吃掉 2 次 429 换 key 自愈，用户无感 |
| 超载 | 3 | 15 | 6 rps | 40%（6.0 rps） | 60% | goodput 精确锁死容量；拒绝 p50=3ms |
| 扩池 | 6 | 15 | 12 rps | 80%（12.0 rps） | 20% | 容量精确翻倍，六 key 全部打满 120 |

延迟全程 avg≈300ms ≈ mock 基础延迟 → 网关自身开销可忽略，瓶颈按设计留在上游配额。

## 测真实厂商（可选）

拿真 key 时只测**拐点**不测极限：从 1 rps 起每档 ×2 爬坡，每档 60s，盯
`tg_gateway_upstream_429_total` 首次非零的档位即真实单 key 容量；配合压低 `max_tokens`
控制 TPM 成本，测完看 `usage_logs` 的 vendor_cost 核对花销。
