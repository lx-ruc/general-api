-- token 中转站数据库 schema（幂等执行；时间统一 unix 秒）
-- 额度单位：点；价格单位：点/1M token；默认 1 元 = 1,000,000 点

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS orgs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL UNIQUE,
  remark      TEXT    NOT NULL DEFAULT '',
  quota_limit INTEGER NOT NULL DEFAULT 0,
  quota_used  INTEGER NOT NULL DEFAULT 0,
  status      INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id        INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
  username      TEXT    NOT NULL UNIQUE,
  password_hash TEXT    NOT NULL,
  display_name  TEXT    NOT NULL DEFAULT '',
  email         TEXT    NOT NULL DEFAULT '',
  role          TEXT    NOT NULL CHECK (role IN ('platform_admin','org_admin','member')),
  quota_limit   INTEGER,
  quota_used    INTEGER NOT NULL DEFAULT 0,
  status        INTEGER NOT NULL DEFAULT 1,
  last_login_at INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
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

CREATE TABLE IF NOT EXISTS models (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,
  display_name TEXT    NOT NULL DEFAULT '',
  vendor       TEXT    NOT NULL DEFAULT '',
  input_price  INTEGER NOT NULL DEFAULT 0,
  output_price INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS api_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id       INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT    NOT NULL DEFAULT '',
  key_prefix   TEXT    NOT NULL,
  key_hash     TEXT    NOT NULL UNIQUE,
  status       INTEGER NOT NULL DEFAULT 1,
  expired_at   INTEGER,
  last_used_at INTEGER,
  created_at   INTEGER NOT NULL
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
  model_name        TEXT    NOT NULL DEFAULT '',
  is_stream         INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  input_price       INTEGER NOT NULL DEFAULT 0,
  output_price      INTEGER NOT NULL DEFAULT 0,
  cost              INTEGER NOT NULL DEFAULT 0,
  no_usage          INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
