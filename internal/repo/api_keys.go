package repo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"workline/internal/domain"
)

// HashAPIKey returns a stable SHA-256 hex digest for the provided key.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// InsertAPIKey stores a hashed API key. KeyHash must already contain the hashed value.
func (r Repo) InsertAPIKey(ctx context.Context, tx *sql.Tx, key domain.APIKey) error {
	if key.ID == "" {
		return errors.New("id required")
	}
	if key.ActorID == "" {
		return errors.New("actor_id required")
	}
	if key.KeyHash == "" {
		return errors.New("key_hash required")
	}
	exec := func(query string, args ...any) (sql.Result, error) {
		if tx != nil {
			return tx.ExecContext(ctx, query, args...)
		}
		return r.DB.ExecContext(ctx, query, args...)
	}
	if key.CreatedAt == "" {
		key.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := exec(`INSERT INTO api_keys(id, actor_id, name, key_hash, created_at) VALUES (?,?,?,?,?)`,
		key.ID, key.ActorID, nullable(key.Name), key.KeyHash, key.CreatedAt)
	return err
}

// GetAPIKeyByHash returns an API key by its hashed value.
func (r Repo) GetAPIKeyByHash(ctx context.Context, hash string) (domain.APIKey, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id, actor_id, COALESCE(name,''), key_hash, created_at FROM api_keys WHERE key_hash=? LIMIT 1`, hash)
	var key domain.APIKey
	var name string
	err := row.Scan(&key.ID, &key.ActorID, &name, &key.KeyHash, &key.CreatedAt)
	if err == sql.ErrNoRows {
		return domain.APIKey{}, ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if name != "" {
		key.Name = name
	}
	return key, nil
}

// ListAPIKeys returns API keys, optionally filtered by actor ID.
func (r Repo) ListAPIKeys(ctx context.Context, actorID string) ([]domain.APIKey, error) {
	query := `SELECT id, actor_id, COALESCE(name,''), key_hash, created_at FROM api_keys`
	var args []any
	if actorID != "" {
		query += ` WHERE actor_id=?`
		args = append(args, actorID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []domain.APIKey
	for rows.Next() {
		var key domain.APIKey
		var name string
		if err := rows.Scan(&key.ID, &key.ActorID, &name, &key.KeyHash, &key.CreatedAt); err != nil {
			return nil, err
		}
		if name != "" {
			key.Name = name
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteAPIKey deletes an API key by ID.
func (r Repo) DeleteAPIKey(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id required")
	}
	_, err := r.DB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}
