PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  kind TEXT CHECK(kind='software-project') NOT NULL,
  status TEXT CHECK(status IN ('active','paused','archived')) NOT NULL,
  description TEXT,
  created_at TEXT NOT NULL
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
  completed_at TEXT
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
  entity_kind TEXT CHECK(entity_kind IN ('project','iteration','task','decision','lease','attestation')) NOT NULL,
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
