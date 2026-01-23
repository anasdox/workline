package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ForbiddenError indicates missing permission.
type ForbiddenError struct {
	Permission string
}

func (e ForbiddenError) Error() string {
	return fmt.Sprintf("permission %s required", e.Permission)
}

// ForbiddenAttestationError indicates missing authority for attestation kind.
type ForbiddenAttestationError struct {
	Kind string
}

func (e ForbiddenAttestationError) Error() string {
	return fmt.Sprintf("attestation authority required for kind %s", e.Kind)
}

// Service provides RBAC helpers backed by SQL.
type Service struct {
	DB *sql.DB
}

func (s Service) EnsureActor(ctx context.Context, tx *sql.Tx, actorID string) error {
	if actorID == "" {
		return errors.New("actor_id required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO actors(id, created_at) VALUES (?,?)`, actorID, now)
	return err
}

func (s Service) ActorHasPermission(ctx context.Context, tx *sql.Tx, projectID, actorID, perm string) (bool, error) {
	row := tx.QueryRowContext(ctx, `
SELECT 1 FROM actor_roles ar
JOIN role_permissions rp ON rp.role_id=ar.role_id
WHERE ar.project_id=? AND ar.actor_id=? AND rp.permission_id=? LIMIT 1`,
		projectID, actorID, perm)
	var n int
	err := row.Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s Service) ActorRoles(ctx context.Context, tx *sql.Tx, projectID, actorID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT role_id FROM actor_roles WHERE project_id=? AND actor_id=?`, projectID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, nil
}

func (s Service) ActorPermissions(ctx context.Context, tx *sql.Tx, projectID, actorID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT rp.permission_id
FROM actor_roles ar
JOIN role_permissions rp ON rp.role_id=ar.role_id
WHERE ar.project_id=? AND ar.actor_id=?`, projectID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var perms []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, nil
}

func (s Service) ActorCanAttest(ctx context.Context, tx *sql.Tx, projectID, actorID, kind string) (bool, error) {
	row := tx.QueryRowContext(ctx, `
SELECT 1 FROM actor_roles ar
JOIN attestation_authorities aa ON aa.role_id=ar.role_id
WHERE ar.project_id=? AND ar.actor_id=? AND aa.project_id=? AND aa.kind=? LIMIT 1`,
		projectID, actorID, projectID, kind)
	var n int
	err := row.Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s Service) ActorAttestationKinds(ctx context.Context, tx *sql.Tx, projectID, actorID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT aa.kind
FROM actor_roles ar
JOIN attestation_authorities aa ON aa.role_id=ar.role_id
WHERE ar.project_id=? AND ar.actor_id=? AND aa.project_id=?`,
		projectID, actorID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		kinds = append(kinds, k)
	}
	return kinds, rows.Err()
}
