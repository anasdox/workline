package repo

import (
	"context"
	"database/sql"
)

func (r Repo) EnsureActor(ctx context.Context, tx *sql.Tx, actorID string, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO actors(id, created_at) VALUES (?,?)`, actorID, now)
	return err
}

func (r Repo) EnsureOrg(ctx context.Context, tx *sql.Tx, orgID, name, now string) error {
	if name == "" {
		name = orgID
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO organizations(id, name, created_at) VALUES (?,?,?)`, orgID, name, now)
	return err
}

func (r Repo) AssignOrgRole(ctx context.Context, tx *sql.Tx, orgID, actorID, role string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO org_roles(org_id, actor_id, role) VALUES (?,?,?)`, orgID, actorID, role)
	return err
}

func (r Repo) InsertRole(ctx context.Context, tx *sql.Tx, id, desc string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO roles(id, description) VALUES (?,?)`, id, desc)
	return err
}

func (r Repo) InsertPermission(ctx context.Context, tx *sql.Tx, id, desc string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO permissions(id, description) VALUES (?,?)`, id, desc)
	return err
}

func (r Repo) AddRolePermission(ctx context.Context, tx *sql.Tx, roleID, permID string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO role_permissions(role_id, permission_id) VALUES (?,?)`, roleID, permID)
	return err
}

func (r Repo) AssignRole(ctx context.Context, tx *sql.Tx, projectID, actorID, roleID string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO actor_roles(project_id, actor_id, role_id) VALUES (?,?,?)`, projectID, actorID, roleID)
	return err
}

func (r Repo) RevokeRole(ctx context.Context, tx *sql.Tx, projectID, actorID, roleID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM actor_roles WHERE project_id=? AND actor_id=? AND role_id=?`, projectID, actorID, roleID)
	return err
}

func (r Repo) AllowAttestationRole(ctx context.Context, tx *sql.Tx, projectID, kind, roleID string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO attestation_authorities(project_id, kind, role_id) VALUES (?,?,?)`, projectID, kind, roleID)
	return err
}

func (r Repo) DenyAttestationRole(ctx context.Context, tx *sql.Tx, projectID, kind, roleID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM attestation_authorities WHERE project_id=? AND kind=? AND role_id=?`, projectID, kind, roleID)
	return err
}

func (r Repo) actorRoles(ctx context.Context, tx *sql.Tx, projectID, actorID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT role_id FROM actor_roles WHERE project_id=? AND actor_id=?`, projectID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

func (r Repo) rolePermissions(ctx context.Context, tx *sql.Tx, roleID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT permission_id FROM role_permissions WHERE role_id=?`, roleID)
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
