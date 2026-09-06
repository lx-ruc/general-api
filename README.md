# token 中转站

国产大模型 API 统一接入与计量计费平台：把厂商 API 转换成自己签发的统一 OpenAI 兼容接口，支持三级账号管理、模型级授权、token 计量计费与额度控制。

## 功能总览

- **三级账号体系**：平台管理员（对接厂商、管理公司、设定价与额度）→ 公司管理员（管理员工、模型授权、额度分配）→ 员工（创建密钥、申请额度、查看用量）
- **API 转换**：对外暴露统一 OpenAI 兼容端点（`/v1/chat/completions`、`/v1/models`），支持 SSE 流式；用户只需把 SDK 的 `base_url` 指向中转站
- **API 路由**：一个 API key 可调用多个模型（请求中的 `model` 参数决定路由），能调哪些模型由公司管理员授权
- **计量计费**：每模型独立设置输入/输出单价（元/百万token），每次调用记录 usage 快照并从员工、公司两级额度同步扣减
- **通用渠道架构**：任何 OpenAI 兼容厂商 = 一条渠道配置（base_url + 上游 key + 模型列表）；预置 DeepSeek / 智谱 GLM / 通义千问
- **高可用**：同模型多渠道按优先级 + 权重负载均衡，失败自动切换备用渠道
- **额度申请审批流**：员工发起 → 公司管理员一键批准（自动追加额度）
- **公司自助注册**：公司名 + 邮箱验证码 + 管理员账号密码，注册后由平台分配额度；额度授权自动邮件通知公司管理员（含追加量、折算金额、备注与最新额度）

## 计费模型

- 额度以 **token 预算**计，默认 `1 元 = 1,000,000 token`（¥2/百万token 的模型即每 token 消耗 2 额度）
- 单次成本 = `ceil((输入tokens × 输入单价 + 输出tokens × 输出单价) / 1,000,000)`，全整数运算无浮点误差
- 分配 = 设上限（`quota_limit`），消费 = 双层同时记账（员工 `quota_used` 与公司 `quota_used` 同事务累加），两级都是硬上限
- 流式请求自动注入 `stream_options.include_usage` 保证计费；上游未返回 usage 时标记不计量

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

生产建议：`TG_JWT_SECRET`/`TG_AES_KEY` 用环境变量注入；前置 Caddy/Nginx 做 HTTPS；`sqlite3 data/token_.db ".backup backup.db"` 定时备份（或 litestream）。

## 首次启动

1. 启动后自动：建表、写入预置渠道与模型（单价 0，需在管理台定价）、创建平台管理员（`config.yaml` 的 `bootstrap_admin_*`，仅 users 表为空时生效）
2. 浏览器打开 `http://<host>:8080/` → 平台管理员登录
3. 【渠道管理】给 DeepSeek/智谱/通义填入厂商 API Key 并启用，点「测试」验证连通；【模型定价】按厂商价目设置单价
4. 【公司管理】新建公司（含首任公司管理员）并分配额度
5. 公司管理员登录 → 【员工管理】创建员工、勾选「模型授权」、分配额度
6. 员工登录 → 【我的密钥】创建 API key（明文仅显示一次）→ 【接入文档】照抄示例接入

## 数据面（给业务方）

```bash
curl http://<host>:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"你好"}]}'
```

openai SDK：`base_url="http://<host>:8080/v1"`，`api_key="sk-..."`。支持 `stream: true`。

错误码：`403 model_not_allowed`（未授权模型）、`429 insufficient_quota`（个人/公司额度耗尽）、`429 rate_limit_error`（默认 60 RPM/密钥）。

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
