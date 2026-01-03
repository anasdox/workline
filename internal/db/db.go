package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const defaultDBName = "proofline.db"

type Config struct {
	Workspace string
}

func dbPath(workspace string) string {
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".proofline", defaultDBName)
}

// EnsureWorkspace creates workspace directory if missing.
func EnsureWorkspace(workspace string) (string, error) {
	path := filepath.Join(workspace, ".proofline")
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// Open opens the SQLite database with foreign keys on.
func Open(cfg Config) (*sql.DB, error) {
	if _, err := EnsureWorkspace(cfg.Workspace); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?cache=shared&_pragma=foreign_keys(1)", dbPath(cfg.Workspace))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Path returns the db path for the workspace.
func Path(workspace string) string {
	return dbPath(workspace)
}
