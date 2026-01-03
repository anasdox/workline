CREATE TABLE IF NOT EXISTS project_configs(
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_project_configs_updated ON project_configs(updated_at);
