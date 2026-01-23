package repo

import (
	"context"
	"database/sql"
	"encoding/json"

	"workline/internal/domain"
)

func (r Repo) CreateValidation(ctx context.Context, v domain.Validation) (domain.Validation, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Validation{}, err
	}
	defer tx.Rollback()
	created, err := r.CreateValidationTx(ctx, tx, v)
	if err != nil {
		return domain.Validation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Validation{}, err
	}
	return created, nil
}

func (r Repo) CreateValidationTx(ctx context.Context, tx *sql.Tx, v domain.Validation) (domain.Validation, error) {
	issues, err := json.Marshal(v.Issues)
	if err != nil {
		return domain.Validation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO validations(id, project_id, task_id, kind, status, summary, issues_json, url, created_by, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.ProjectID, v.TaskID, v.Kind, v.Status, nullableString(v.Summary), string(issues), nullableString(v.URL), v.CreatedBy, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return domain.Validation{}, err
	}
	return v, nil
}

func (r Repo) UpdateValidation(ctx context.Context, v domain.Validation) (domain.Validation, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Validation{}, err
	}
	defer tx.Rollback()
	updated, err := r.UpdateValidationTx(ctx, tx, v)
	if err != nil {
		return domain.Validation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Validation{}, err
	}
	return updated, nil
}

func (r Repo) UpdateValidationTx(ctx context.Context, tx *sql.Tx, v domain.Validation) (domain.Validation, error) {
	issues, err := json.Marshal(v.Issues)
	if err != nil {
		return domain.Validation{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE validations SET kind=?, status=?, summary=?, issues_json=?, url=?, updated_at=? WHERE id=?`,
		v.Kind, v.Status, nullableString(v.Summary), string(issues), nullableString(v.URL), v.UpdatedAt, v.ID)
	if err != nil {
		return domain.Validation{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.Validation{}, ErrNotFound
	}
	return r.GetValidationTx(ctx, tx, v.ID)
}

func (r Repo) GetValidation(ctx context.Context, id string) (domain.Validation, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Validation{}, err
	}
	defer tx.Rollback()
	v, err := r.GetValidationTx(ctx, tx, id)
	if err != nil {
		return domain.Validation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Validation{}, err
	}
	return v, nil
}

func (r Repo) GetValidationTx(ctx context.Context, tx *sql.Tx, id string) (domain.Validation, error) {
	var v domain.Validation
	var summary, issuesJSON, url sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, project_id, task_id, kind, status, summary, issues_json, url, created_by, created_at, updated_at
FROM validations WHERE id=?`, id).
		Scan(&v.ID, &v.ProjectID, &v.TaskID, &v.Kind, &v.Status, &summary, &issuesJSON, &url, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if summary.Valid {
		v.Summary = summary.String
	}
	if url.Valid {
		v.URL = url.String
	}
	if issuesJSON.Valid && issuesJSON.String != "" {
		_ = json.Unmarshal([]byte(issuesJSON.String), &v.Issues)
	}
	return v, nil
}

func (r Repo) ListValidationsByTask(ctx context.Context, projectID, taskID string) ([]domain.Validation, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, project_id, task_id, kind, status, summary, issues_json, url, created_by, created_at, updated_at
FROM validations WHERE project_id=? AND task_id=? ORDER BY created_at ASC, id ASC`, projectID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Validation
	for rows.Next() {
		var v domain.Validation
		var summary, issuesJSON, url sql.NullString
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.TaskID, &v.Kind, &v.Status, &summary, &issuesJSON, &url, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if summary.Valid {
			v.Summary = summary.String
		}
		if url.Valid {
			v.URL = url.String
		}
		if issuesJSON.Valid && issuesJSON.String != "" {
			_ = json.Unmarshal([]byte(issuesJSON.String), &v.Issues)
		}
		res = append(res, v)
	}
	return res, rows.Err()
}

func (r Repo) HasRejectedValidation(ctx context.Context, projectID, taskID string) (bool, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT 1 FROM validations WHERE project_id=? AND task_id=? AND status='rejected' LIMIT 1`,
		projectID, taskID)
	var n int
	err := row.Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
