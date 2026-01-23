package repo

import (
	"context"
	"database/sql"
	"time"

	"workline/internal/config"
	"workline/internal/domain"
)

func (r Repo) UpsertActorMission(ctx context.Context, projectID, actorID, mission string) (domain.ActorMission, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.ActorMission{}, err
	}
	defer tx.Rollback()
	am, err := r.UpsertActorMissionTx(ctx, tx, projectID, actorID, mission)
	if err != nil {
		return domain.ActorMission{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ActorMission{}, err
	}
	return am, nil
}

func (r Repo) UpsertActorMissionTx(ctx context.Context, tx *sql.Tx, projectID, actorID, mission string) (domain.ActorMission, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.EnsureActor(ctx, tx, actorID, now); err != nil {
		return domain.ActorMission{}, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO actor_missions(project_id, actor_id, mission, created_at, updated_at)
VALUES (?,?,?,?,?)
ON CONFLICT(project_id, actor_id) DO UPDATE SET mission=excluded.mission, updated_at=excluded.updated_at`,
		projectID, actorID, mission, now, now)
	if err != nil {
		return domain.ActorMission{}, err
	}
	return r.GetActorMissionTx(ctx, tx, projectID, actorID)
}

func (r Repo) GetActorMission(ctx context.Context, projectID, actorID string) (domain.ActorMission, error) {
	var am domain.ActorMission
	err := r.DB.QueryRowContext(ctx, `SELECT project_id, actor_id, mission, created_at, updated_at FROM actor_missions WHERE project_id=? AND actor_id=?`,
		projectID, actorID).Scan(&am.ProjectID, &am.ActorID, &am.Mission, &am.CreatedAt, &am.UpdatedAt)
	if err == sql.ErrNoRows {
		return am, ErrNotFound
	}
	return am, err
}

func (r Repo) GetActorMissionTx(ctx context.Context, tx *sql.Tx, projectID, actorID string) (domain.ActorMission, error) {
	var am domain.ActorMission
	err := tx.QueryRowContext(ctx, `SELECT project_id, actor_id, mission, created_at, updated_at FROM actor_missions WHERE project_id=? AND actor_id=?`,
		projectID, actorID).Scan(&am.ProjectID, &am.ActorID, &am.Mission, &am.CreatedAt, &am.UpdatedAt)
	if err == sql.ErrNoRows {
		return am, ErrNotFound
	}
	return am, err
}

func (r Repo) ListActorMissions(ctx context.Context, projectID, actorID string) ([]domain.ActorMission, error) {
	query := `SELECT project_id, actor_id, mission, created_at, updated_at FROM actor_missions WHERE project_id=?`
	args := []any{projectID}
	if actorID != "" {
		query += " AND actor_id=?"
		args = append(args, actorID)
	}
	query += " ORDER BY actor_id ASC"
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.ActorMission
	for rows.Next() {
		var am domain.ActorMission
		if err := rows.Scan(&am.ProjectID, &am.ActorID, &am.Mission, &am.CreatedAt, &am.UpdatedAt); err != nil {
			return nil, err
		}
		res = append(res, am)
	}
	return res, rows.Err()
}

func (r Repo) DeleteActorMission(ctx context.Context, projectID, actorID string) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM actor_missions WHERE project_id=? AND actor_id=?`, projectID, actorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r Repo) DeleteActorMissionTx(ctx context.Context, tx *sql.Tx, projectID, actorID string) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM actor_missions WHERE project_id=? AND actor_id=?`, projectID, actorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r Repo) ReplaceActorMissions(ctx context.Context, projectID string, missions []config.ActorMissionConfig) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.ReplaceActorMissionsTx(ctx, tx, projectID, missions); err != nil {
		return err
	}
	return tx.Commit()
}

func (r Repo) ReplaceActorMissionsTx(ctx context.Context, tx *sql.Tx, projectID string, missions []config.ActorMissionConfig) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM actor_missions WHERE project_id=?`, projectID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range missions {
		if err := r.EnsureActor(ctx, tx, m.ActorID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO actor_missions(project_id, actor_id, mission, created_at, updated_at) VALUES (?,?,?,?,?)`,
			projectID, m.ActorID, m.Mission, now, now); err != nil {
			return err
		}
	}
	return nil
}
