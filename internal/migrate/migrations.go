package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed sql/*.sql
var migrationsFS embed.FS

type Migration struct {
	Version int
	Name    string
	UpSQL   string
}

func loadMigrations() ([]Migration, error) {
	files, err := fs.ReadDir(migrationsFS, "sql")
	if err != nil {
		return nil, err
	}
	var migrations []Migration
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		data, err := migrationsFS.ReadFile("sql/" + f.Name())
		if err != nil {
			return nil, err
		}
		var v int
		_, err = fmt.Sscanf(f.Name(), "%d_", &v)
		if err != nil {
			return nil, fmt.Errorf("invalid migration filename %s: %w", f.Name(), err)
		}
		migrations = append(migrations, Migration{
			Version: v,
			Name:    f.Name(),
			UpSQL:   string(data),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Migrate applies embedded migrations in order.
func Migrate(db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL);`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var currentVersion int
	err = tx.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&currentVersion)
	if err == sql.ErrNoRows {
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (0)`); err != nil {
			return fmt.Errorf("init schema_version: %w", err)
		}
		currentVersion = 0
	} else if err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}
		if _, err := tx.Exec(m.UpSQL); err != nil {
			return fmt.Errorf("migration %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version=?`, m.Version); err != nil {
			return fmt.Errorf("update schema_version: %w", err)
		}
		currentVersion = m.Version
	}
	return tx.Commit()
}
