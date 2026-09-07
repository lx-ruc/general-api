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
  status         INTEGER NOT NULL DEFAULT 1,
  last_login_at  INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_org ON users(org_id);

CREATE TABLE IF NOT EXISTS channels (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  name             TEXT    NOT NULL UNIQUE,
  vendor           TEXT    NOT NULL DEFAULT '',
  base_url         TEXT    NOT NULL,
  path             TEXT    NOT NULL DEFAULT '/v1/chat/completions',
  upstream_key_enc TEXT    NOT NULL DEFAULT '',
  weight           INTEGER NOT NULL DEFAULT 1,
  priority         INTEGER NOT NULL DEFAULT 0,
  status           INTEGER NOT NULL DEFAULT 1,
  last_test_at     INTEGER,
  last_test_ok     INTEGER NOT NULL DEFAULT 0,
  remark           TEXT    NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_keys (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  key_enc    TEXT    NOT NULL,               -- AES-GCM 密文（未启用加密则明文）
  weight     INTEGER NOT NULL DEFAULT 1,     -- 池内调度权重
  status     INTEGER NOT NULL DEFAULT 1,     -- 0=禁用（401 自动禁用 / 管理员手动）
  remark     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_channel_keys_channel ON channel_keys(channel_id, status);

CREATE TABLE IF NOT EXISTS models (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,
  display_name TEXT    NOT NULL DEFAULT '',
  vendor       TEXT    NOT NULL DEFAULT '',
  input_price  INTEGER NOT NULL DEFAULT 0,
  output_price INTEGER NOT NULL DEFAULT 0,
  cost_input_price  INTEGER NOT NULL DEFAULT 0,   -- 厂商成本价（毛利核算）
  cost_output_price INTEGER NOT NULL DEFAULT 0,
  status       INTEGER NOT NULL DEFAULT 1,
  remark       TEXT    NOT NULL DEFAULT '',
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
  input_price       INTEGER NOT NULL DEFAULT 0,  -- 结算时快照（售卖价）
  output_price      INTEGER NOT NULL DEFAULT 0,
  cost_input_price  INTEGER NOT NULL DEFAULT 0,  -- 成本价快照
  cost_output_price INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS quota_requests (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  amount     INTEGER NOT NULL,
  reason     TEXT    NOT NULL DEFAULT '',
  status     TEXT    NOT NULL DEFAULT 'pending',
  handled_by INTEGER,
  handled_at INTEGER,
  reply      TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_qreq_org  ON quota_requests(org_id, status);
CREATE INDEX IF NOT EXISTS idx_qreq_user ON quota_requests(user_id);

CREATE TABLE IF NOT EXISTS verification_codes (
  email     TEXT PRIMARY KEY,
  code      TEXT    NOT NULL,
  expire_at INTEGER NOT NULL,
  sent_at   INTEGER NOT NULL,
  attempts  INTEGER NOT NULL DEFAULT 0
);

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
