-- API keys for automation/agents
CREATE TABLE IF NOT EXISTS api_keys(
  id TEXT PRIMARY KEY,
  actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  name TEXT,
  key_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
