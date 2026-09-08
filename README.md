# token 中转站

国产大模型 API 统一接入与计量计费平台：把厂商 API 转换成自己签发的统一 OpenAI 兼容接口，支持三级账号管理、模型级授权、token 计量计费与额度控制。

## 功能总览

- **三级账号体系**：系统管理员（对接厂商、管理客户、设定价与额度）→ 客户管理员（管理子账号、模型授权、额度分配）→ 子账号（创建密钥、申请额度、查看用量）
- **API 转换**：对外暴露统一 OpenAI 兼容端点（`/v1/chat/completions`、`/v1/models`），支持 SSE 流式；用户只需把 SDK 的 `base_url` 指向中转站
- **API 路由**：一个 API key 可调用多个模型（请求中的 `model` 参数决定路由），能调哪些模型由客户管理员授权
- **计量计费**：每模型独立设置输入/输出单价（元/百万token），每次调用记录 usage 快照并从子账号、客户两级额度同步扣减
- **通用渠道架构**：任何 OpenAI 兼容厂商 = 一条渠道配置（base_url + 上游 key + 模型列表）；预置 DeepSeek / 智谱 GLM / 通义千问
- **高可用**：同模型多渠道按优先级 + 权重负载均衡，失败自动切换备用渠道
- **额度申请审批流**：子账号发起 → 客户管理员一键批准（自动追加额度）
- **客户自助注册**：客户名 + 邮箱验证码 + 管理员账号密码，注册后由平台分配额度；额度授权自动邮件通知客户管理员（含追加量、折算金额、备注与最新额度）
- **成本中心归集**：客户维护受控中心词表（如「AI客服」「数据分析」），API key 挂靠中心；结算时把中心快照写进 usage_logs——改派只影响未来，历史账单不可变；org 报表按中心×模型×日聚合、未归集恒置底披露占比；平台可看跨客户中心毛利交叉报表
- **额度预警**：客户/子账号两级水位告警，结算后异步检查（60s 节流、多实例 CAS 唯一投递）；每档只提醒一次、追加额度后自动复位；客户耗尽（100%）同时通知系统管理员跟进续费；阈值可按客户设置（0=关闭，默认 80%），告警邮件含水位与直达链接（`server.site_url`）
- **月度对账单**：三段式（勾稽/冲减/明细），月末余额快照次月 1 日 00:05（`billing.timezone`，默认 Asia/Shanghai）自动写入、漏跑重启自愈，可平台手动补跑；勾稽链 `期末used − 期初used == 期内消耗` 自动校验并显式标 ✗；负数授权即冲减回收入冲减段；明细按 模型×成本中心×日 聚合（未归集置底，含缓存命中列）；CSV 带 BOM 可直接双击打开；org 视角永不携带厂商成本/毛利；另有厂商账单对账页（录入厂商账单，|差异|>2% 标红，披露不计量笔数）
- **单月上限与欠费停服**：客户/子账号可设单月消费上限（token 预算，0=不限），当月达限拦截（429 `monthly_limit_exceeded`）、次月自动清零恢复（结算路径原子重置，无需定时任务）；客户总额度耗尽自动转「欠费停服」（数据面 403 `insufficient_balance`，管理台仍可登录），平台追加额度即自动恢复——与手动停用互不干扰；客户开户支持联系人/联系电话
- **在线体验**：管理台顶栏对话框选模型直接流式试聊，注入合成身份复用 `/v1` 完整编排（多 Key 池/排队/熔断/SSE/计费全一致）；子账号按个人授权、客户管理员按客户授权并集、系统管理员可试全部可路由模型且不计费；对话内容不落审计与日志（计量走 usage_logs，KeyID=0 标识）
- **文档中心**：顶栏「文档」进入内置文档站（产品介绍/快速开始/核心功能 + OpenAI 兼容 API 参考：参数表、cURL/Python/Node 示例、错误码一览），全部角色可读；接入示例的 base_url 自动跟随当前部署地址

## 计费模型

- 额度以 **token 预算**计，默认 `1 元 = 1,000,000 token`（¥2/百万token 的模型即每 token 消耗 2 额度）
- 单次成本 = `ceil((输入tokens × 输入单价 + 输出tokens × 输出单价) / 1,000,000)`，全整数运算无浮点误差
- 分配 = 设上限（`quota_limit`），消费 = 双层同时记账（子账号 `quota_used` 与客户 `quota_used` 同事务累加），两级都是硬上限
- 流式请求自动注入 `stream_options.include_usage` 保证计费；上游未返回 usage 时标记不计量
- **额度审计不变量**：quota_limit 的一切变更（建号初始/追加/充值/设值切换）均与 QuotaGrant 流水同事务，Σgrants == COALESCE(limit, 0) 恒成立

## 快速开始

### 本地开发

```bash
# 后端（Go ≥ 1.22）
cp config.example.yaml config.yaml   # 修改 jwt_secret 与管理员密码
make dev                             # 监听 :8080

# 前端（Node ≥ 18，另开终端）
make web-install
make web-dev                         # Vite :5173，代理 /api /v1 到 :8080
```

### 构建单二进制（前端 embed 进 Go）

```bash
make build          # 产物 dist/token-gateway，单文件部署，只需要 config.yaml + data/ 目录
```

### Linux 服务器部署

```bash
make cross-build    # 产物 dist/token-gateway-linux-amd64（CGO_ENABLED=0，无依赖）
# 上传二进制 + config.example.yaml 到 /opt/token-gateway/，复制 systemd unit：
sudo cp deploy/token-gateway.service /etc/systemd/system/
sudo systemctl enable --now token-gateway
```

生产建议：`TG_JWT_SECRET`/`TG_AES_KEY` 用环境变量注入；前置 Caddy/Nginx 做 HTTPS；`sh deploy/backup.sh` 定时备份（cron 建议 `0 4 * * *`，保留最近 7 份）。

## 高可用部署（对外服务）

单机 SQLite 适合内部使用；**对外卖服务请用 PG 模式 + 双网关**（几百并发下单机实测 2000+ RPS，20 倍余量）：

```bash
cd deploy
# .env 里放 PG_PASSWORD / TG_JWT_SECRET / TG_ADMIN_PASSWORD
docker compose up -d          # postgres + redis + gateway×2 + caddy（80/443，轮询+健康检查）
```

- **切 PG 搬存量数据**：`TG_DATABASE_DRIVER=postgres TG_DATABASE_DSN="..." ./token-gateway -migrate-from-sqlite data/token_.db`
- **滚动发布**（不停机）：`docker compose up -d --no-deps --build gateway1`，健康检查通过后再发 gateway2
- **监控**：`GET /metrics` 输出 Prometheus 指标（请求量/延迟直方图/活跃流/上游错误/熔断/上游 429/Key 冷却/排队时长/缓存命中等）
- **渠道熔断**：某渠道连续失败 N 次（`gateway.channel_breaker_threshold`，默认 5，0=关）自动禁用并写备注，修复后手动启用
- **高并发协调器**（`internal/coord`）：渠道多 Key 池 + 429 冷却自动换 Key + 渠道并发闸门（排队削峰）+ 非流式精确缓存（命中不扣费）；compose 内置 Redis 全局共享，Redis 故障 fail-open 不拖死数据面
- Caddyfile 改成你的域名即自动 HTTPS；SSE 已配 `flush_interval -1` 透传
- per-key 限流仍为 per-node 内存令牌桶：双实例下单 key 实际上限约为配置值 × 节点数（几百客户场景可接受；需精确全局限流时引入 Redis）

架构与压测数据详见 `docs/高并发设计.md`。

## 首次启动

1. 启动后自动：建表、写入预置渠道与模型（单价 0，需在管理台定价）、创建系统管理员（`config.yaml` 的 `bootstrap_admin_*`，仅 users 表为空时生效）
2. 浏览器打开 `http://<host>:8080/` → 系统管理员登录
3. 【渠道管理】给 DeepSeek/智谱/通义填入厂商 API Key 并启用，点「测试」验证连通；【模型定价】按厂商价目设置单价
4. 【客户管理】新建客户（含首任客户管理员）并分配额度
5. 客户管理员登录 → 【子账号管理】创建子账号、勾选「模型授权」、分配额度
6. 子账号登录 → 【我的密钥】创建 API key（明文仅显示一次）→ 【接入文档】照抄示例接入

## 数据面（给业务方）

```bash
curl http://<host>:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"你好"}]}'
```

openai SDK：`base_url="http://<host>:8080/v1"`，`api_key="sk-..."`。支持 `stream: true`。

错误码：`403 model_not_allowed`（未授权模型）、`429 insufficient_quota`（个人/客户额度耗尽）、`429 rate_limit_error`（默认 60 RPM/密钥）。

## 架构

```
main.go                     入口：配置→DB→迁移→seed→路由→优雅退出
internal/
  config/                   yaml + TG_* 环境变量
  database/                 SQLite（pure-Go，WAL，_txlock=immediate，单连接串行化）
  model/                    GORM 实体（11 张表）
  auth/                     bcrypt / JWT(HS256) / API key 生成与 SHA-256 哈希
  crypto/                   上游密钥 AES-256-GCM 加密存储（可选）
  middleware/               JWT 鉴权 / RBAC / API key 鉴权 / 令牌桶限流
  service/                  quota（额度唯一写入口：预检查/结算/拨备）· stats · bootstrap
  gateway/                  数据面：渠道选择(优先级+权重) → 转发(SSE 逐块 flush + usage 提取) → 计费结算
  api/{platform,org,member}/ 三级角色 REST handler（org 强制 WHERE 隔离）
  webui/                    前端静态 + SPA fallback
web/                        Vue3 + Element Plus + ECharts 管理台
```

## 安全要点

- 密码 bcrypt；API key 明文不落库不写日志（SHA-256 唯一索引 O(1) 查找）
- 上游厂商 key AES-256-GCM 加密存储，接口永不回显（留空 = 不修改）
- 三层越权防护：JWT 角色守卫 + org 隔离强制 WHERE + 对象归属校验
- 登录限速 5 次/分/IP；请求体 10MB 上限；调用日志只记计量元数据不落对话内容
