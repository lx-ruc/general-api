-- token 中转站数据库 schema（幂等执行；时间统一 unix 秒）
-- 额度单位：点；价格单位：点/1M token；默认 1 元 = 1,000,000 点

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS orgs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT    NOT NULL UNIQUE,
  remark              TEXT    NOT NULL DEFAULT '',
  contact_name        TEXT    NOT NULL DEFAULT '', -- 客户联系人
  contact_phone       TEXT    NOT NULL DEFAULT '', -- 联系电话
  quota_limit         INTEGER NOT NULL DEFAULT 0,
  quota_used          INTEGER NOT NULL DEFAULT 0,
  monthly_quota       INTEGER NOT NULL DEFAULT 0,  -- 单月消费上限（点；0=不限）
  monthly_cost        INTEGER NOT NULL DEFAULT 0,  -- 当前账期累计（惰性跨月清零）
  monthly_period      TEXT    NOT NULL DEFAULT '', -- monthly_cost 所属账期 'YYYY-MM'（账期时区）
  status              INTEGER NOT NULL DEFAULT 1, -- 1启用 0停用 2欠费停服（额度耗尽自动置，充值自动恢复）
  require_cost_center INTEGER NOT NULL DEFAULT 0,  -- 1=新建 key 必须归集成本中心
  alert_levels        TEXT    NOT NULL DEFAULT '[80]', -- 预警阈值（升序百分比 JSON 数组；[] = 关闭）
  alert_level         INTEGER NOT NULL DEFAULT 0,  -- 当前已达档位（0=未达任何档；边沿状态机）
  alert_since         INTEGER NOT NULL DEFAULT 0,  -- 进入当前档位的时间
  alert_balance       INTEGER NOT NULL DEFAULT 0,  -- 剩余额度预警阈值（点；0=关闭。与百分比档正交：任一命中即告警）
  alert_balance_hit   INTEGER NOT NULL DEFAULT 0,  -- 金额档命中时刻（0=未命中；边沿状态机，拨备回落惰性复位）
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id         INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
  username       TEXT    NOT NULL UNIQUE,
  password_hash  TEXT    NOT NULL,
  display_name   TEXT    NOT NULL DEFAULT '',
  email          TEXT    NOT NULL DEFAULT '',
  role           TEXT    NOT NULL CHECK (role IN ('platform_admin','org_admin','member')),
  quota_limit    INTEGER,
  quota_used     INTEGER NOT NULL DEFAULT 0,
  monthly_quota  INTEGER NOT NULL DEFAULT 0,  -- 单月消费上限（点；0=不限）
  monthly_cost   INTEGER NOT NULL DEFAULT 0,  -- 当前账期累计（惰性跨月清零）
  monthly_period TEXT    NOT NULL DEFAULT '',
  alert_levels   TEXT    NOT NULL DEFAULT '[80]', -- 个人预警阈值（[] = 关闭；不限额者不参与）
  alert_level    INTEGER NOT NULL DEFAULT 0,
  alert_since    INTEGER NOT NULL DEFAULT 0,
  alert_balance     INTEGER NOT NULL DEFAULT 0, -- 个人剩余额度预警阈值（点；0=关闭）
  alert_balance_hit INTEGER NOT NULL DEFAULT 0, -- 金额档命中时刻（0=未命中）
  is_super      INTEGER NOT NULL DEFAULT 0, -- 平台层超管标志（1=超级管理员；仅 role=platform_admin 有意义）
  status         INTEGER NOT NULL DEFAULT 1,
  last_login_at  INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_org ON users(org_id);

CREATE TABLE IF NOT EXISTS channels (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  name             TEXT    NOT NULL UNIQUE,
  vendor_id        INTEGER NOT NULL DEFAULT 0, -- 所属厂商（0=未挂，不可路由）
  base_url         TEXT    NOT NULL,
  path             TEXT    NOT NULL DEFAULT '/v1/chat/completions',
  weight           INTEGER NOT NULL DEFAULT 1,
  priority         INTEGER NOT NULL DEFAULT 0,
  status           INTEGER NOT NULL DEFAULT 1,
  auto_disabled_at INTEGER NOT NULL DEFAULT 0,  -- >0=系统熔断禁用时间（自动探测恢复）；0=未禁用或人工禁用
  nokey_disabled_at INTEGER NOT NULL DEFAULT 0, -- >0=因无可用密钥自动停用的时间（补 Key 自动恢复）；0=未停用/其他原因停用
  last_test_at     INTEGER,
  last_test_ok     INTEGER NOT NULL DEFAULT 0,
  remark           TEXT    NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,
  display_name TEXT    NOT NULL DEFAULT '',
  vendor_id    INTEGER NOT NULL DEFAULT 0, -- 归属厂商（0=未挂；授权/厂商价按此展开）
  type         TEXT    NOT NULL DEFAULT 'chat', -- chat/embedding/video：决定端点路由与探测形态
  input_price  INTEGER NOT NULL DEFAULT 0,
  output_price INTEGER NOT NULL DEFAULT 0,
  input_cache_hit_price  INTEGER NOT NULL DEFAULT 0,  -- 缓存命中输入单价（0=同 input_price）
  cost_input_price  INTEGER NOT NULL DEFAULT 0,   -- 厂商成本价（毛利核算）
  cost_output_price INTEGER NOT NULL DEFAULT 0,
  cost_input_cache_hit_price INTEGER NOT NULL DEFAULT 0, -- 成本侧缓存命中价（0=同成本输入价）
  status       INTEGER NOT NULL DEFAULT 1,
  remark       TEXT    NOT NULL DEFAULT '',
  auto_disabled_at INTEGER NOT NULL DEFAULT 0,  -- >0=因上游密钥/渠道不可用自动停用的时间（换 Key 后自动恢复）；0=手动停用
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_abilities (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id          INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model_name          TEXT    NOT NULL,
  upstream_model_name TEXT,
  UNIQUE (channel_id, model_name)
);
CREATE INDEX IF NOT EXISTS idx_abilities_model ON channel_abilities(model_name);

CREATE TABLE IF NOT EXISTS user_model_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  model_name TEXT    NOT NULL,
  granted_by INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE (user_id, model_name)
);
CREATE INDEX IF NOT EXISTS idx_grants_user ON user_model_grants(user_id);

-- 成本中心词表（org 内受控；status 0=归档，永不硬删，历史引用保留）
CREATE TABLE IF NOT EXISTS cost_centers (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  status     INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
CREATE INDEX IF NOT EXISTS idx_cost_centers_org ON cost_centers(org_id, status);

CREATE TABLE IF NOT EXISTS api_keys (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id         INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name           TEXT    NOT NULL DEFAULT '',
  key_prefix     TEXT    NOT NULL,
  key_hash       TEXT    NOT NULL UNIQUE,
  key_enc        TEXT    NOT NULL DEFAULT '',  -- 完整明文 AES-GCM 密文（复制完整密钥用；''=功能上线前存量；鉴权仍走 key_hash）
  status         INTEGER NOT NULL DEFAULT 1,
  expired_at     INTEGER,
  last_used_at   INTEGER,
  cost_center_id INTEGER REFERENCES cost_centers(id),  -- 归集中心（结算时快照进 usage_logs）
  created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_keys_user   ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_keys_prefix ON api_keys(key_prefix);

CREATE TABLE IF NOT EXISTS usage_logs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id        TEXT    NOT NULL DEFAULT '',
  org_id            INTEGER NOT NULL,
  user_id           INTEGER NOT NULL,
  api_key_id        INTEGER NOT NULL,
  channel_id        INTEGER,
  cost_center_id    INTEGER,                  -- 结算时快照（改派不动历史）
  model_name        TEXT    NOT NULL DEFAULT '',
  is_stream         INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens     INTEGER NOT NULL DEFAULT 0,  -- 上游提示缓存命中的输入 tokens（计入 prompt_tokens）
  input_price       INTEGER NOT NULL DEFAULT 0,  -- 结算时快照（售卖价）
  output_price      INTEGER NOT NULL DEFAULT 0,
  input_cache_hit_price INTEGER NOT NULL DEFAULT 0, -- 快照（0=同输入价）
  cost_input_price  INTEGER NOT NULL DEFAULT 0,  -- 成本价快照
  cost_output_price INTEGER NOT NULL DEFAULT 0,
  cost_input_cache_hit_price INTEGER NOT NULL DEFAULT 0,
  vendor_cost       INTEGER NOT NULL DEFAULT 0,  -- 厂商成本（毛利 = cost - vendor_cost）
  cost              INTEGER NOT NULL DEFAULT 0,  -- 客户扣减（= 平台营收）
  no_usage          INTEGER NOT NULL DEFAULT 0,
  cache_hit         INTEGER NOT NULL DEFAULT 0,   -- 1=精确缓存命中（未打上游，cost=0）
  status            INTEGER NOT NULL DEFAULT 0,
  error             TEXT    NOT NULL DEFAULT '',
  latency_ms        INTEGER NOT NULL DEFAULT 0,
  client_ip         TEXT    NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_created ON usage_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_org     ON usage_logs(org_id, created_at);
CREATE INDEX IF NOT EXISTS idx_logs_user    ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_logs_model   ON usage_logs(model_name);

CREATE TABLE IF NOT EXISTS quota_grants (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  subject_type TEXT    NOT NULL CHECK (subject_type IN ('org','user')),
  subject_id   INTEGER NOT NULL,
  amount       INTEGER NOT NULL,
  remark       TEXT    NOT NULL DEFAULT '',
  operator_id  INTEGER,
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_qgrants_subject ON quota_grants(subject_type, subject_id);

-- 月末余额快照：period_balances(P) = P 月末时点的 org limit/used。
-- 账单勾稽：期初[M] = snapshot[M-1]，期末[M] = snapshot[M]（当月实时值兜底）；
-- 链式自证 snapshot[M].used − snapshot[M−1].used == Σcost(M)（used 单调，grants 不动 used）
CREATE TABLE IF NOT EXISTS period_balances (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id      INTEGER NOT NULL,
  period      TEXT    NOT NULL,               -- 'YYYY-MM'（该月末时点）
  quota_limit INTEGER NOT NULL DEFAULT 0,
  quota_used  INTEGER NOT NULL DEFAULT 0,
  points_per_yuan INTEGER NOT NULL DEFAULT 0, -- 快照时点的点/元汇率（0=未记录，账单折元回退当前值）
  snapshot_at INTEGER NOT NULL,               -- 实际写入时刻（月末 00:05 或补跑时刻）
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE(org_id, period)
);

-- 厂商账单手工录入：与 usage_logs Σvendor_cost（按渠道）对差异，>2% 标红
CREATE TABLE IF NOT EXISTS vendor_bills (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  period        TEXT    NOT NULL,             -- 'YYYY-MM'
  channel_id    INTEGER NOT NULL,
  billed_points INTEGER NOT NULL,             -- 厂商账单金额（点数口径）
  note          TEXT    NOT NULL DEFAULT '',
  created_by    INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE(period, channel_id)
);

-- 子账号申请单（额度申请 kind=quota / 模型授权申请 kind=model，客户管理员审批）
CREATE TABLE IF NOT EXISTS quota_requests (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id      INTEGER NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL DEFAULT 'quota', -- quota=额度 model=模型授权
  amount      INTEGER NOT NULL DEFAULT 0,       -- kind=quota 时的申请点数
  model_names TEXT    NOT NULL DEFAULT '',      -- kind=model 时的申请模型名（逗号分隔）
  reason      TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL DEFAULT 'pending',
  handled_by  INTEGER,
  handled_at  INTEGER,
  reply       TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_qreq_org  ON quota_requests(org_id, status);
CREATE INDEX IF NOT EXISTS idx_qreq_user ON quota_requests(user_id);

-- 客户级差异化定价：同一模型对不同客户可设不同售卖价（无覆盖行走 models 默认价）。
-- 只有售卖三价随客户覆盖；厂商成本价（models.cost_*）保持模型级不变。
CREATE TABLE IF NOT EXISTS org_model_prices (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id                INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  model_name            TEXT    NOT NULL,
  input_price           INTEGER NOT NULL DEFAULT 0,
  output_price          INTEGER NOT NULL DEFAULT 0,
  input_cache_hit_price INTEGER NOT NULL DEFAULT 0, -- 0=同输入价（与 models 口径一致）
  remark                TEXT    NOT NULL DEFAULT '',
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE(org_id, model_name)
);
CREATE INDEX IF NOT EXISTS idx_org_model_prices_org ON org_model_prices(org_id);

-- 客户级模型白名单：平台开通（审批/手动）后客户才能给子账号授权该模型；
-- 无记录=未开通，回收=删行（与 user_model_grants 同语义）。
-- source: grant=审批/手动开通，backfill=存量继承（首次升级自动写入）。
CREATE TABLE IF NOT EXISTS org_model_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  model_name TEXT    NOT NULL,
  granted_by INTEGER,
  source     TEXT    NOT NULL DEFAULT 'grant',
  created_at INTEGER NOT NULL,
  UNIQUE(org_id, model_name)
);
CREATE INDEX IF NOT EXISTS idx_org_grants_org ON org_model_grants(org_id);

-- 客户向平台申请开通模型（platform_admin 审批），形状同 recharge_requests
CREATE TABLE IF NOT EXISTS org_model_requests (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id      INTEGER NOT NULL,
  model_names TEXT    NOT NULL DEFAULT '',
  reason      TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL DEFAULT 'pending',
  handled_by  INTEGER,
  handled_at  INTEGER,
  reply       TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_omreq_org ON org_model_requests(org_id, status);

CREATE TABLE IF NOT EXISTS verification_codes (
  email     TEXT PRIMARY KEY,
  code      TEXT    NOT NULL,
  expire_at INTEGER NOT NULL,
  sent_at   INTEGER NOT NULL,
  attempts  INTEGER NOT NULL DEFAULT 0
);

-- 密码重置令牌：明文 token 只出现在邮件链接里，SHA-256 落库（UNIQUE 查找）；
-- 30 分钟有效、一次性（used_at 原子置位防并发双用）
CREATE TABLE IF NOT EXISTS password_reset_tokens (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email      TEXT    NOT NULL,
  token_hash TEXT    NOT NULL UNIQUE,
  expire_at  INTEGER NOT NULL,
  used_at    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- 邮箱绑定/换绑验证码：与注册验证码(verification_codes)分表——后者以 email 为 PK、
-- 无 purpose 维度（改 PK 需存量迁移）。绑定验证以 user 为主体：同一邮箱可能同时被
-- 多个账号发起绑定，各验各的；每次发码先作废该用户旧码（以最新邮件为准）
CREATE TABLE IF NOT EXISTS email_bind_codes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email      TEXT    NOT NULL,
  code       TEXT    NOT NULL,
  expire_at  INTEGER NOT NULL,
  sent_at    INTEGER NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_bind_user ON email_bind_codes(user_id);

CREATE TABLE IF NOT EXISTS recharge_requests (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL,
  amount     INTEGER NOT NULL,
  voucher    TEXT    NOT NULL DEFAULT '',
  status     TEXT    NOT NULL DEFAULT 'pending',
  handled_by INTEGER,
  handled_at INTEGER,
  reply      TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_recharge_org ON recharge_requests(org_id, status);

CREATE TABLE IF NOT EXISTS audit_logs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id   INTEGER,
  actor      TEXT    NOT NULL DEFAULT '',
  method     TEXT    NOT NULL DEFAULT '',
  path       TEXT    NOT NULL DEFAULT '',
  status     INTEGER NOT NULL DEFAULT 0,
  detail     TEXT    NOT NULL DEFAULT '',
  ip         TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS access_tokens (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL,
  name        TEXT    NOT NULL DEFAULT '',
  token_hash  TEXT    NOT NULL UNIQUE,
  token_enc   TEXT    NOT NULL DEFAULT '',  -- 完整明文 AES-GCM 密文（复制用；''=功能上线前存量）
  prefix      TEXT    NOT NULL DEFAULT '',
  status      INTEGER NOT NULL DEFAULT 1,
  expires_at  INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_access_tokens_user ON access_tokens(user_id);

-- 站内通知（目前用于：Key 配额冷却告警等需要系统管理员处理的运营事件）
CREATE TABLE IF NOT EXISTS notifications (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,             -- 接收人（系统管理员），每人一行独立已读状态
  type       TEXT    NOT NULL DEFAULT '',  -- 事件类型（key_quota_cooling）
  title      TEXT    NOT NULL DEFAULT '',
  body       TEXT    NOT NULL DEFAULT '',
  payload    TEXT    NOT NULL DEFAULT '',  -- JSON 附件（channel_id / key_id 等，绝不放密钥明文）
  read_at    INTEGER NOT NULL DEFAULT 0,   -- 0=未读
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, read_at, id);

-- 视频生成任务登记：创建时快照归属/渠道/价格，客户查询或补偿扫描到 succeeded 时结算。
-- billed 是结算幂等抢占位：UPDATE … WHERE billed=0 的行数判定保证双网关并发只结一次
CREATE TABLE IF NOT EXISTS video_tasks (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id        TEXT    NOT NULL UNIQUE,       -- 上游任务 id（Ark cgt-…）；结算幂等锚点
  org_id         INTEGER NOT NULL,
  user_id        INTEGER NOT NULL,              -- 结算锚定创建者（非查询者）
  api_key_id     INTEGER NOT NULL,
  cost_center_id INTEGER,                       -- 创建时快照，结算进 usage_logs
  channel_id     INTEGER NOT NULL,              -- 查询按此取渠道配置与密钥（无外键，同 usage_logs 口径）
  model_name     TEXT    NOT NULL DEFAULT '',   -- 对外模型名
  status         TEXT    NOT NULL DEFAULT '',   -- 最近观测的上游状态（''=创建后未查询过）
  billed         INTEGER NOT NULL DEFAULT 0,    -- 1=已结算
  completion_tokens INTEGER NOT NULL DEFAULT 0, -- 结算时 usage 快照（对账用）
  output_price   INTEGER NOT NULL DEFAULT 0,    -- 创建时售卖价快照（已含客户覆盖价）
  cost_output_price INTEGER NOT NULL DEFAULT 0, -- 创建时成本价快照
  create_request_id TEXT NOT NULL DEFAULT '',   -- 关联创建那条 cost=0 的 usage_log
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_video_tasks_sweep ON video_tasks(billed, created_at);

-- 厂商实体：Key 池 / 渠道(端点) / 模型 / 授权 / 定价的归属主体。
-- status=0 主开关：该厂商全部端点视为不可路由
CREATE TABLE IF NOT EXISTS vendors (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,      -- 厂商标识（迁移自 channels.vendor 标签，如 volces、volces/Thu）
  display_name TEXT    NOT NULL DEFAULT '',  -- 展示名（空=用 name）
  status       INTEGER NOT NULL DEFAULT 1,
  remark       TEXT    NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

-- 厂商 Key 池（channel_keys 上提）：厂商下全部端点共用，轮询/冷却/禁用同现有机制；
-- key_enc 密文自 channel_keys 原样搬迁（AES-GCM，不经过加解密）
CREATE TABLE IF NOT EXISTS vendor_keys (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  vendor_id  INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
  key_enc    TEXT    NOT NULL,
  weight     INTEGER NOT NULL DEFAULT 1,
  status     INTEGER NOT NULL DEFAULT 1, -- 0=禁用（401/402 自动禁用 / 手动）
  remark     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vendor_keys_vendor ON vendor_keys(vendor_id, status);

-- 客户级厂商开通：授厂商 ⟺ 组内全部模型可用（组内新模型自动继承，数据面按
-- models.vendor_id 展开判定）；回收=删行+级联子账号厂商授权
CREATE TABLE IF NOT EXISTS org_vendor_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  vendor_id  INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
  granted_by INTEGER,
  source     TEXT    NOT NULL DEFAULT 'grant', -- grant=审批/手动 backfill=存量折算
  created_at INTEGER NOT NULL,
  UNIQUE(org_id, vendor_id)
);
CREATE INDEX IF NOT EXISTS idx_org_vendor_grants_org ON org_vendor_grants(org_id);

-- 子账号级厂商授权（数据面放行主口径；user_model_grants 保留为模型级例外）
CREATE TABLE IF NOT EXISTS user_vendor_grants (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  vendor_id  INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
  granted_by INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE(user_id, vendor_id)
);
CREATE INDEX IF NOT EXISTS idx_user_vendor_grants_user ON user_vendor_grants(user_id);

-- 客户×厂商一套三价（组内模型统一生效；模型级覆盖 org_model_prices 优先级更高；
-- 生效价 = 模型覆盖 > 客户厂商价 > 模型默认价）
CREATE TABLE IF NOT EXISTS org_vendor_prices (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id                INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  vendor_id             INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
  input_price           INTEGER NOT NULL DEFAULT 0,
  output_price          INTEGER NOT NULL DEFAULT 0,
  input_cache_hit_price INTEGER NOT NULL DEFAULT 0, -- 0=同输入价（与 models 口径一致）
  remark                TEXT    NOT NULL DEFAULT '',
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE(org_id, vendor_id)
);
CREATE INDEX IF NOT EXISTS idx_org_vendor_prices_org ON org_vendor_prices(org_id);
