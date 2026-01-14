PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS organizations(
  id TEXT PRIMARY KEY,
  name TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS actors(
  id TEXT PRIMARY KEY,
  display_name TEXT,
  kind TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS roles(
  id TEXT PRIMARY KEY,
  description TEXT
);

CREATE TABLE IF NOT EXISTS permissions(
  id TEXT PRIMARY KEY,
  description TEXT
);

CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind TEXT CHECK(kind='software-project') NOT NULL,
  status TEXT CHECK(status IN ('active','paused','archived')) NOT NULL,
  description TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS project_configs(
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS iterations(
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  goal TEXT NOT NULL,
  status TEXT CHECK(status IN ('pending','running','delivered','validated','rejected')) NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks(
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  iteration_id TEXT REFERENCES iterations(id) ON DELETE SET NULL,
  parent_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
  type TEXT CHECK(type IN ('technical','feature','bug','docs','chore','workshop')) NOT NULL,
  title TEXT NOT NULL,
  description TEXT,
  status TEXT CHECK(status IN ('planned','in_progress','review','done','rejected','canceled')) NOT NULL,
  assignee_id TEXT,
  priority INTEGER,
  work_outcomes_json TEXT,
  required_attestations_json TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  work_proof_json TEXT,
  validation_mode TEXT,
  required_threshold INTEGER
);

CREATE TABLE IF NOT EXISTS task_deps(
  task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
  depends_on_task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
  PRIMARY KEY(task_id, depends_on_task_id)
);

CREATE TABLE IF NOT EXISTS decisions(
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  context_json TEXT,
  decision TEXT NOT NULL,
  rationale_json TEXT,
  alternatives_json TEXT,
  decider_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS leases(
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  owner_id TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS attestations(
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  entity_kind TEXT CHECK(entity_kind IN ('project','iteration','task','decision')) NOT NULL,
  entity_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  ts TEXT NOT NULL,
  payload_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_attestations_entity ON attestations(entity_kind, entity_id);
CREATE INDEX IF NOT EXISTS idx_attestations_kind ON attestations(kind);

CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  type TEXT NOT NULL,
  project_id TEXT,
  entity_kind TEXT CHECK(entity_kind IN ('project','iteration','task','decision','lease','attestation','rbac')) NOT NULL,
  entity_id TEXT,
  actor_id TEXT NOT NULL,
  payload_json TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_iterations_project ON iterations(project_id);
CREATE INDEX IF NOT EXISTS idx_iterations_status ON iterations(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_iteration ON tasks(iteration_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id);
CREATE INDEX IF NOT EXISTS idx_leases_expires ON leases(expires_at);
CREATE INDEX IF NOT EXISTS idx_events_project ON events(project_id);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);

CREATE TABLE IF NOT EXISTS role_permissions(
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id TEXT NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  PRIMARY KEY(role_id, permission_id)
);
CREATE INDEX IF NOT EXISTS idx_role_perms_role ON role_permissions(role_id);

CREATE TABLE IF NOT EXISTS actor_roles(
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY(project_id, actor_id, role_id)
);
CREATE INDEX IF NOT EXISTS idx_actor_roles_actor ON actor_roles(project_id, actor_id);

CREATE TABLE IF NOT EXISTS attestation_authorities(
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY(project_id, kind, role_id)
);
CREATE INDEX IF NOT EXISTS idx_att_auth_kind ON attestation_authorities(project_id, kind);

CREATE TABLE IF NOT EXISTS api_keys(
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  name TEXT,
  key_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_project_configs_updated ON project_configs(updated_at);

CREATE TABLE IF NOT EXISTS org_roles(
  org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  PRIMARY KEY(org_id, actor_id)
);

INSERT OR IGNORE INTO organizations(id, name, created_at)
VALUES ('default-org','Default Org',strftime('%Y-%m-%dT%H:%M:%SZ','now'));
