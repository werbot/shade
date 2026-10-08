CREATE TABLE IF NOT EXISTS projects (
  id         INTEGER PRIMARY KEY,
  root_path  TEXT NOT NULL UNIQUE,      -- git-root, fallback cwd
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rules (
  id           INTEGER PRIMARY KEY,
  project_id   INTEGER REFERENCES projects(id) ON DELETE CASCADE,  -- NULL = global
  name         TEXT NOT NULL,
  type         TEXT NOT NULL,           -- type of the placeholder
  kind         TEXT NOT NULL,           -- regex | literal | entropy
  pattern      TEXT NOT NULL,
  secret_group INTEGER NOT NULL DEFAULT 0,
  keywords     TEXT NOT NULL DEFAULT '[]',   -- JSON array of prefilter literals
  entropy_min  REAL NOT NULL DEFAULT 0,
  validator    TEXT NOT NULL DEFAULT '',
  allowlist    TEXT NOT NULL DEFAULT '[]',   -- JSON array of regexps
  order_idx    INTEGER NOT NULL DEFAULT 100,
  enabled      INTEGER NOT NULL DEFAULT 1,
  builtin      INTEGER NOT NULL DEFAULT 0,
  package_id   INTEGER REFERENCES packages(id),
  updated_at   INTEGER NOT NULL,
  CHECK (kind IN ('regex', 'literal', 'entropy'))
);

-- project_id IS NULL = a global rule. UNIQUE(project_id, name) does not
-- work here: in SQLite NULLs are not equal to each other, and global duplicates would slip through.
CREATE UNIQUE INDEX IF NOT EXISTS rules_global_name  ON rules(name) WHERE project_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS rules_project_name ON rules(project_id, name) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS entities (
  id             INTEGER PRIMARY KEY,
  project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  type           TEXT NOT NULL,
  value_enc      BLOB NOT NULL,          -- AES-256-GCM
  value_hash     BLOB NOT NULL,          -- HMAC-SHA256(key, type ‖ value)
  placeholder    TEXT NOT NULL,
  hits           INTEGER NOT NULL DEFAULT 1,
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  UNIQUE(project_id, value_hash),
  UNIQUE(project_id, placeholder)
);

CREATE TABLE IF NOT EXISTS counters (
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  next_number INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (project_id, type)
);

CREATE TABLE IF NOT EXISTS packages (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  version      TEXT NOT NULL,
  source       TEXT NOT NULL,
  installed_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rule_hits (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  rule       TEXT NOT NULL,
  day        TEXT NOT NULL,             -- YYYY-MM-DD
  count      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, rule, day)
);

CREATE TABLE IF NOT EXISTS audit (
  id         INTEGER PRIMARY KEY,
  ts         INTEGER NOT NULL,
  project_id INTEGER,
  direction  TEXT NOT NULL,             -- to_model | from_model
  adapter    TEXT NOT NULL,             -- hook | mcp | proxy | cli
  rule       TEXT,
  type       TEXT,
  action     TEXT NOT NULL,             -- unresolved | blocked
  detail     TEXT
);

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
