package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"workline/internal/config"
	"workline/internal/domain"
)

type Repo struct {
	DB *sql.DB
}

var ErrNotFound = errors.New("not found")

func scanProject(row *sql.Row) (domain.Project, error) {
	var p domain.Project
	var desc sql.NullString
	err := row.Scan(&p.ID, &p.OrgID, &p.Kind, &p.Status, &desc, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return p, ErrNotFound
	}
	if desc.Valid {
		p.Description = desc.String
	}
	return p, err
}

func (r Repo) InsertProject(ctx context.Context, p domain.Project) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO projects(id,org_id,kind,status,description,created_at) VALUES (?,?,?,?,?,?)`,
		p.ID, p.OrgID, p.Kind, p.Status, nullable(p.Description), p.CreatedAt)
	return err
}

func (r Repo) GetProject(ctx context.Context, id string) (domain.Project, error) {
	return scanProject(r.DB.QueryRowContext(ctx, `SELECT id,org_id,kind,status,COALESCE(description,'') AS description,created_at FROM projects WHERE id=?`, id))
}

func (r Repo) SingleProject(ctx context.Context) (domain.Project, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,org_id,kind,status,COALESCE(description,'') AS description,created_at FROM projects`)
	if err != nil {
		return domain.Project{}, err
	}
	defer rows.Close()
	var projects []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Kind, &p.Status, &p.Description, &p.CreatedAt); err != nil {
			return domain.Project{}, err
		}
		projects = append(projects, p)
	}
	if len(projects) == 0 {
		return domain.Project{}, ErrNotFound
	}
	if len(projects) > 1 {
		return domain.Project{}, fmt.Errorf("multiple projects exist; specify --project")
	}
	return projects[0], nil
}

func (r Repo) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,org_id,kind,status,COALESCE(description,'') AS description,created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Kind, &p.Status, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, nil
}

func (r Repo) InsertIteration(ctx context.Context, it domain.Iteration) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO iterations(id,project_id,goal,status,created_at) VALUES (?,?,?,?,?)`,
		it.ID, it.ProjectID, it.Goal, it.Status, it.CreatedAt)
	return err
}

func (r Repo) InsertIterationTx(ctx context.Context, tx *sql.Tx, it domain.Iteration) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO iterations(id,project_id,goal,status,created_at) VALUES (?,?,?,?,?)`,
		it.ID, it.ProjectID, it.Goal, it.Status, it.CreatedAt)
	return err
}

func (r Repo) UpdateProject(ctx context.Context, id, status string, description *string) error {
	var (
		fields []string
		args   []any
	)
	if status != "" {
		fields = append(fields, "status=?")
		args = append(args, status)
	}
	if description != nil {
		fields = append(fields, "description=?")
		args = append(args, nullable(*description))
	}
	if len(fields) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := r.DB.ExecContext(ctx, fmt.Sprintf(`UPDATE projects SET %s WHERE id=?`, strings.Join(fields, ",")), args...)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r Repo) DeleteProject(ctx context.Context, id string) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r Repo) UpsertProjectConfig(ctx context.Context, projectID string, cfg *config.Config) error {
	return upsertProjectConfig(ctx, r.DB, nil, projectID, cfg)
}

func (r Repo) UpsertProjectConfigTx(ctx context.Context, tx *sql.Tx, projectID string, cfg *config.Config) error {
	return upsertProjectConfig(ctx, nil, tx, projectID, cfg)
}

func upsertProjectConfig(ctx context.Context, db *sql.DB, tx *sql.Tx, projectID string, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config nil")
	}
	cfg.Project.ID = projectID
	if err := cfg.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	exec := func(query string, args ...any) (sql.Result, error) {
		if tx != nil {
			return tx.ExecContext(ctx, query, args...)
		}
		return db.ExecContext(ctx, query, args...)
	}
	_, err = exec(`INSERT INTO project_configs(project_id,config_json,created_at,updated_at) VALUES (?,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET config_json=excluded.config_json, updated_at=excluded.updated_at`, projectID, string(payload), now, now)
	return err
}

func (r Repo) GetProjectConfig(ctx context.Context, projectID string) (*config.Config, error) {
	var payload string
	err := r.DB.QueryRowContext(ctx, `SELECT config_json FROM project_configs WHERE project_id=?`, projectID).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(payload), &cfg); err != nil {
		return nil, err
	}
	if cfg.Project.ID == "" {
		cfg.Project.ID = projectID
	}
	return &cfg, cfg.Validate()
}

func (r Repo) ListIterations(ctx context.Context, projectID string) ([]domain.Iteration, error) {
	return r.ListIterationsWithCursor(ctx, projectID, 0, "", "")
}

func (r Repo) ListIterationsWithCursor(ctx context.Context, projectID string, limit int, cursorCreatedAt, cursorID string) ([]domain.Iteration, error) {
	clauses := []string{"project_id=?"}
	args := []any{projectID}
	if cursorCreatedAt != "" && cursorID != "" {
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, cursorCreatedAt, cursorCreatedAt, cursorID)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")
	query := `SELECT id,project_id,goal,status,created_at FROM iterations ` + where + ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Iteration
	for rows.Next() {
		var it domain.Iteration
		if err := rows.Scan(&it.ID, &it.ProjectID, &it.Goal, &it.Status, &it.CreatedAt); err != nil {
			return nil, err
		}
		res = append(res, it)
	}
	return res, nil
}

func (r Repo) GetIteration(ctx context.Context, id string) (domain.Iteration, error) {
	var it domain.Iteration
	err := r.DB.QueryRowContext(ctx, `SELECT id,project_id,goal,status,created_at FROM iterations WHERE id=?`, id).
		Scan(&it.ID, &it.ProjectID, &it.Goal, &it.Status, &it.CreatedAt)
	if err == sql.ErrNoRows {
		return it, ErrNotFound
	}
	return it, err
}

func (r Repo) UpdateIterationStatus(ctx context.Context, tx *sql.Tx, id, status string) error {
	_, err := tx.ExecContext(ctx, `UPDATE iterations SET status=? WHERE id=?`, status, id)
	return err
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableStringPtr(v *string) any {
	if v == nil {
		return nil
	}
	if *v == "" {
		return nil
	}
	return *v
}

func (r Repo) InsertTask(ctx context.Context, tx *sql.Tx, t domain.Task) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,iteration_id,parent_id,type,title,description,status,assignee_id,priority,work_outcomes_json,required_attestations_json,created_at,updated_at,completed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ProjectID, nullableStringPtr(t.IterationID), nullableStringPtr(t.ParentID), t.Type, t.Title, nullable(t.Description),
		t.Status, nullableStringPtr(t.AssigneeID), nullableIntPtr(t.Priority), nullableStringPtr(t.WorkOutcomesJSON), nullableStringPtr(t.RequiredAttestationsJSON),
		t.CreatedAt, t.UpdatedAt, nullableStringPtr(t.CompletedAt))
	return err
}

func (r Repo) UpdateTask(ctx context.Context, tx *sql.Tx, t domain.Task) error {
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET iteration_id=?, parent_id=?, type=?, title=?, description=?, status=?, assignee_id=?, priority=?, work_outcomes_json=?, required_attestations_json=?, updated_at=?, completed_at=? WHERE id=?`,
		nullableStringPtr(t.IterationID), nullableStringPtr(t.ParentID), t.Type, t.Title, nullable(t.Description), t.Status,
		nullableStringPtr(t.AssigneeID), nullableIntPtr(t.Priority), nullableStringPtr(t.WorkOutcomesJSON), nullableStringPtr(t.RequiredAttestationsJSON),
		t.UpdatedAt, nullableStringPtr(t.CompletedAt), t.ID)
	return err
}

func (r Repo) GetTask(ctx context.Context, id string) (domain.Task, error) {
	var t domain.Task
	var iterationID, parentID, assigneeID, workOutcomes, requiredAtt, completedAt, description sql.NullString
	var priority sql.NullInt64
	err := r.DB.QueryRowContext(ctx, `SELECT id,project_id,iteration_id,parent_id,type,title,description,status,assignee_id,priority,work_outcomes_json,required_attestations_json,created_at,updated_at,completed_at FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.ProjectID, &iterationID, &parentID, &t.Type, &t.Title, &description, &t.Status, &assigneeID, &priority, &workOutcomes, &requiredAtt, &t.CreatedAt, &t.UpdatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if description.Valid {
		t.Description = description.String
	}
	if iterationID.Valid {
		t.IterationID = &iterationID.String
	}
	if parentID.Valid {
		t.ParentID = &parentID.String
	}
	if assigneeID.Valid {
		t.AssigneeID = &assigneeID.String
	}
	if priority.Valid {
		p := int(priority.Int64)
		t.Priority = &p
	}
	if workOutcomes.Valid {
		t.WorkOutcomesJSON = &workOutcomes.String
	}
	if requiredAtt.Valid {
		t.RequiredAttestationsJSON = &requiredAtt.String
	}
	if completedAt.Valid {
		t.CompletedAt = &completedAt.String
	}
	deps, err := r.ListTaskDependencies(ctx, t.ID)
	if err != nil {
		return t, err
	}
	t.DependsOn = deps
	return t, err
}

func (r Repo) GetTaskTx(ctx context.Context, tx *sql.Tx, id string) (domain.Task, error) {
	var t domain.Task
	var iterationID, parentID, assigneeID, workOutcomes, requiredAtt, completedAt, description sql.NullString
	var priority sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,iteration_id,parent_id,type,title,description,status,assignee_id,priority,work_outcomes_json,required_attestations_json,created_at,updated_at,completed_at FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.ProjectID, &iterationID, &parentID, &t.Type, &t.Title, &description, &t.Status, &assigneeID, &priority, &workOutcomes, &requiredAtt, &t.CreatedAt, &t.UpdatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if description.Valid {
		t.Description = description.String
	}
	if iterationID.Valid {
		t.IterationID = &iterationID.String
	}
	if parentID.Valid {
		t.ParentID = &parentID.String
	}
	if assigneeID.Valid {
		t.AssigneeID = &assigneeID.String
	}
	if priority.Valid {
		p := int(priority.Int64)
		t.Priority = &p
	}
	if workOutcomes.Valid {
		t.WorkOutcomesJSON = &workOutcomes.String
	}
	if requiredAtt.Valid {
		t.RequiredAttestationsJSON = &requiredAtt.String
	}
	if completedAt.Valid {
		t.CompletedAt = &completedAt.String
	}
	deps, err := r.ListTaskDependenciesTx(ctx, tx, t.ID)
	if err != nil {
		return t, err
	}
	t.DependsOn = deps
	return t, nil
}

type TaskFilters struct {
	ProjectID       string
	Status          string
	Iteration       string
	Parent          string
	AssigneeID      string
	Limit           int
	CursorCreatedAt string
	CursorID        string
}

type NextTaskFilters struct {
	ProjectID         string
	IterationID       string
	AssigneeID        string
	IncludeUnassigned bool
}

func (r Repo) ListTasks(ctx context.Context, f TaskFilters) ([]domain.Task, error) {
	var clauses []string
	var args []any
	if f.ProjectID != "" {
		clauses = append(clauses, "project_id=?")
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		clauses = append(clauses, "status=?")
		args = append(args, f.Status)
	}
	if f.Iteration != "" {
		clauses = append(clauses, "iteration_id=?")
		args = append(args, f.Iteration)
	}
	if f.Parent != "" {
		clauses = append(clauses, "parent_id=?")
		args = append(args, f.Parent)
	}
	if f.AssigneeID != "" {
		clauses = append(clauses, "assignee_id=?")
		args = append(args, f.AssigneeID)
	}
	if f.CursorCreatedAt != "" && f.CursorID != "" {
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, f.CursorCreatedAt, f.CursorCreatedAt, f.CursorID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	query := `SELECT id,project_id,iteration_id,parent_id,type,title,description,status,assignee_id,priority,work_outcomes_json,required_attestations_json,created_at,updated_at,completed_at FROM tasks ` + where + ` ORDER BY created_at DESC, id DESC`
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Task
	for rows.Next() {
		var t domain.Task
		var iterationID, parentID, assigneeID, workOutcomes, requiredAtt, completedAt, description sql.NullString
		var priority sql.NullInt64
		if err := rows.Scan(&t.ID, &t.ProjectID, &iterationID, &parentID, &t.Type, &t.Title, &description, &t.Status, &assigneeID, &priority, &workOutcomes, &requiredAtt, &t.CreatedAt, &t.UpdatedAt, &completedAt); err != nil {
			return nil, err
		}
		if description.Valid {
			t.Description = description.String
		}
		if iterationID.Valid {
			t.IterationID = &iterationID.String
		}
		if parentID.Valid {
			t.ParentID = &parentID.String
		}
		if assigneeID.Valid {
			t.AssigneeID = &assigneeID.String
		}
		if priority.Valid {
			p := int(priority.Int64)
			t.Priority = &p
		}
		if workOutcomes.Valid {
			t.WorkOutcomesJSON = &workOutcomes.String
		}
		if requiredAtt.Valid {
			t.RequiredAttestationsJSON = &requiredAtt.String
		}
		if completedAt.Valid {
			t.CompletedAt = &completedAt.String
		}
		res = append(res, t)
	}
	return res, nil
}

func (r Repo) NextTask(ctx context.Context, f NextTaskFilters) (domain.Task, error) {
	var t domain.Task
	if f.ProjectID == "" || f.IterationID == "" {
		return t, ErrNotFound
	}
	clauses := []string{"project_id=?", "iteration_id=?", "status=?"}
	args := []any{f.ProjectID, f.IterationID, "planned"}
	if f.AssigneeID != "" {
		if f.IncludeUnassigned {
			clauses = append(clauses, "(assignee_id=? OR assignee_id IS NULL)")
			args = append(args, f.AssigneeID)
		} else {
			clauses = append(clauses, "assignee_id=?")
			args = append(args, f.AssigneeID)
		}
	} else if !f.IncludeUnassigned {
		clauses = append(clauses, "assignee_id IS NOT NULL")
	}
	clauses = append(clauses, `NOT EXISTS (
		SELECT 1 FROM task_deps d
		JOIN tasks dep ON dep.id=d.depends_on_task_id
		WHERE d.task_id=tasks.id AND dep.status != 'done'
	)`)
	where := "WHERE " + strings.Join(clauses, " AND ")
	order := `ORDER BY
		CASE WHEN assignee_id = ? THEN 0 ELSE 1 END,
		CASE WHEN priority IS NULL THEN 1 ELSE 0 END,
		priority ASC,
		created_at ASC,
		id ASC`
	if f.AssigneeID == "" {
		order = `ORDER BY
			CASE WHEN priority IS NULL THEN 1 ELSE 0 END,
			priority ASC,
			created_at ASC,
			id ASC`
	} else {
		args = append(args, f.AssigneeID)
	}
	query := `SELECT id,project_id,iteration_id,parent_id,type,title,description,status,assignee_id,priority,work_outcomes_json,required_attestations_json,created_at,updated_at,completed_at FROM tasks ` + where + " " + order + " LIMIT 1"
	var iterationID, parentID, assigneeID, workOutcomes, requiredAtt, completedAt, description sql.NullString
	var priority sql.NullInt64
	err := r.DB.QueryRowContext(ctx, query, args...).
		Scan(&t.ID, &t.ProjectID, &iterationID, &parentID, &t.Type, &t.Title, &description, &t.Status, &assigneeID, &priority, &workOutcomes, &requiredAtt, &t.CreatedAt, &t.UpdatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if description.Valid {
		t.Description = description.String
	}
	if iterationID.Valid {
		t.IterationID = &iterationID.String
	}
	if parentID.Valid {
		t.ParentID = &parentID.String
	}
	if assigneeID.Valid {
		t.AssigneeID = &assigneeID.String
	}
	if priority.Valid {
		p := int(priority.Int64)
		t.Priority = &p
	}
	if workOutcomes.Valid {
		t.WorkOutcomesJSON = &workOutcomes.String
	}
	if requiredAtt.Valid {
		t.RequiredAttestationsJSON = &requiredAtt.String
	}
	if completedAt.Valid {
		t.CompletedAt = &completedAt.String
	}
	deps, err := r.ListTaskDependencies(ctx, t.ID)
	if err != nil {
		return t, err
	}
	t.DependsOn = deps
	return t, nil
}

func (r Repo) ListTaskDependencies(ctx context.Context, taskID string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT depends_on_task_id FROM task_deps WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func (r Repo) ListTaskDependenciesTx(ctx context.Context, tx *sql.Tx, taskID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT depends_on_task_id FROM task_deps WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func (r Repo) AddDependencies(ctx context.Context, tx *sql.Tx, taskID string, deps []string) error {
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on_task_id) VALUES (?,?)`, taskID, d); err != nil {
			return err
		}
	}
	return nil
}

func (r Repo) RemoveDependencies(ctx context.Context, tx *sql.Tx, taskID string, deps []string) error {
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps WHERE task_id=? AND depends_on_task_id=?`, taskID, d); err != nil {
			return err
		}
	}
	return nil
}

func (r Repo) ListChildren(ctx context.Context, taskID string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id FROM tasks WHERE parent_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r Repo) ListChildrenTx(ctx context.Context, tx *sql.Tx, taskID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE parent_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r Repo) UpsertLease(ctx context.Context, tx *sql.Tx, lease domain.Lease) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO leases(task_id,owner_id,acquired_at,expires_at) VALUES (?,?,?,?)
ON CONFLICT(task_id) DO UPDATE SET owner_id=excluded.owner_id, acquired_at=excluded.acquired_at, expires_at=excluded.expires_at`, lease.TaskID, lease.OwnerID, lease.AcquiredAt, lease.ExpiresAt)
	return err
}

func (r Repo) DeleteLease(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM leases WHERE task_id=?`, taskID)
	return err
}

func (r Repo) GetLeaseTx(ctx context.Context, tx *sql.Tx, taskID string) (domain.Lease, error) {
	var l domain.Lease
	err := tx.QueryRowContext(ctx, `SELECT task_id,owner_id,acquired_at,expires_at FROM leases WHERE task_id=?`, taskID).
		Scan(&l.TaskID, &l.OwnerID, &l.AcquiredAt, &l.ExpiresAt)
	if err == sql.ErrNoRows {
		return l, ErrNotFound
	}
	return l, err
}

func (r Repo) GetLease(ctx context.Context, taskID string) (domain.Lease, error) {
	var l domain.Lease
	err := r.DB.QueryRowContext(ctx, `SELECT task_id,owner_id,acquired_at,expires_at FROM leases WHERE task_id=?`, taskID).
		Scan(&l.TaskID, &l.OwnerID, &l.AcquiredAt, &l.ExpiresAt)
	if err == sql.ErrNoRows {
		return l, ErrNotFound
	}
	return l, err
}

func (r Repo) InsertAttestation(ctx context.Context, att domain.Attestation) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO attestations(id,project_id,entity_kind,entity_id,kind,actor_id,ts,payload_json) VALUES (?,?,?,?,?,?,?,?)`,
		att.ID, att.ProjectID, att.EntityKind, att.EntityID, att.Kind, att.ActorID, att.TS, nullable(att.PayloadJSON))
	return err
}

func (r Repo) InsertAttestationTx(ctx context.Context, tx *sql.Tx, att domain.Attestation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO attestations(id,project_id,entity_kind,entity_id,kind,actor_id,ts,payload_json) VALUES (?,?,?,?,?,?,?,?)`,
		att.ID, att.ProjectID, att.EntityKind, att.EntityID, att.Kind, att.ActorID, att.TS, nullable(att.PayloadJSON))
	return err
}

type AttestationFilters struct {
	EntityKind string
	EntityID   string
	Kind       string
	ProjectID  string
	Limit      int
	CursorTS   string
	CursorID   string
}

func (r Repo) ListAttestations(ctx context.Context, f AttestationFilters) ([]domain.Attestation, error) {
	var clauses []string
	var args []any
	if f.ProjectID != "" {
		clauses = append(clauses, "project_id=?")
		args = append(args, f.ProjectID)
	}
	if f.EntityKind != "" {
		clauses = append(clauses, "entity_kind=?")
		args = append(args, f.EntityKind)
	}
	if f.EntityID != "" {
		clauses = append(clauses, "entity_id=?")
		args = append(args, f.EntityID)
	}
	if f.Kind != "" {
		clauses = append(clauses, "kind=?")
		args = append(args, f.Kind)
	}
	if f.CursorTS != "" && f.CursorID != "" {
		clauses = append(clauses, "(ts < ? OR (ts = ? AND id < ?))")
		args = append(args, f.CursorTS, f.CursorTS, f.CursorID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	query := `SELECT id,project_id,entity_kind,entity_id,kind,actor_id,ts,payload_json FROM attestations ` + where + ` ORDER BY ts DESC, id DESC`
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Attestation
	for rows.Next() {
		var a domain.Attestation
		var payload sql.NullString
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.EntityKind, &a.EntityID, &a.Kind, &a.ActorID, &a.TS, &payload); err != nil {
			return nil, err
		}
		if payload.Valid {
			a.PayloadJSON = payload.String
		}
		res = append(res, a)
	}
	return res, nil
}

func (r Repo) CountTasksByStatus(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT status, count(*) FROM tasks WHERE project_id=? GROUP BY status`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		res[status] = count
	}
	return res, nil
}

func (r Repo) LatestRunningIteration(ctx context.Context, projectID string) (*domain.Iteration, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id,project_id,goal,status,created_at FROM iterations WHERE project_id=? AND status='running' ORDER BY created_at DESC LIMIT 1`, projectID)
	var it domain.Iteration
	err := row.Scan(&it.ID, &it.ProjectID, &it.Goal, &it.Status, &it.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (r Repo) LatestEvents(ctx context.Context, limit int, projectID, evtType, entityKind, entityID string) ([]domain.Event, error) {
	return r.LatestEventsFrom(ctx, limit, 0, projectID, evtType, entityKind, entityID)
}

func (r Repo) LatestEventsFrom(ctx context.Context, limit int, cursor int64, projectID, evtType, entityKind, entityID string) ([]domain.Event, error) {
	clauses := []string{"1=1"}
	var args []any
	if projectID != "" {
		clauses = append(clauses, "project_id=?")
		args = append(args, projectID)
	}
	if evtType != "" {
		clauses = append(clauses, "type=?")
		args = append(args, evtType)
	}
	if entityKind != "" {
		clauses = append(clauses, "entity_kind=?")
		args = append(args, entityKind)
	}
	if entityID != "" {
		clauses = append(clauses, "entity_id=?")
		args = append(args, entityID)
	}
	if cursor > 0 {
		clauses = append(clauses, "id<?")
		args = append(args, cursor)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")
	query := fmt.Sprintf(`SELECT id,ts,type,project_id,entity_kind,entity_id,actor_id,payload_json FROM events %s ORDER BY id DESC LIMIT ?`, where)
	args = append(args, limit)
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Event
	for rows.Next() {
		var e domain.Event
		var payload sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &e.Type, &e.ProjectID, &e.EntityKind, &e.EntityID, &e.ActorID, &payload); err != nil {
			return nil, err
		}
		if payload.Valid {
			e.Payload = payload.String
		}
		res = append(res, e)
	}
	return res, nil
}

// EventsAfter returns events with IDs greater than the cursor in ascending order.
func (r Repo) EventsAfter(ctx context.Context, limit int, cursor int64, projectID string) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	clauses := []string{"1=1"}
	var args []any
	if projectID != "" {
		clauses = append(clauses, "project_id=?")
		args = append(args, projectID)
	}
	if cursor > 0 {
		clauses = append(clauses, "id>?")
		args = append(args, cursor)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")
	query := fmt.Sprintf(`SELECT id,ts,type,project_id,entity_kind,entity_id,actor_id,payload_json FROM events %s ORDER BY id ASC LIMIT ?`, where)
	args = append(args, limit)
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []domain.Event
	for rows.Next() {
		var e domain.Event
		var payload sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &e.Type, &e.ProjectID, &e.EntityKind, &e.EntityID, &e.ActorID, &payload); err != nil {
			return nil, err
		}
		if payload.Valid {
			e.Payload = payload.String
		}
		res = append(res, e)
	}
	return res, nil
}

// LatestEventID returns the most recent event ID for a project.
func (r Repo) LatestEventID(ctx context.Context, projectID string) (int64, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM events WHERE project_id=?`, projectID)
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func nullableIntPtr(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func (r Repo) InsertDecision(ctx context.Context, d domain.Decision) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO decisions(id,project_id,title,context_json,decision,rationale_json,alternatives_json,decider_id,created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, d.Title, nullable(d.ContextJSON), d.Decision, nullable(d.RationaleJSON), nullable(d.AlternativesJSON), d.DeciderID, d.CreatedAt)
	return err
}

func (r Repo) InsertDecisionTx(ctx context.Context, tx *sql.Tx, d domain.Decision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO decisions(id,project_id,title,context_json,decision,rationale_json,alternatives_json,decider_id,created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, d.Title, nullable(d.ContextJSON), d.Decision, nullable(d.RationaleJSON), nullable(d.AlternativesJSON), d.DeciderID, d.CreatedAt)
	return err
}
