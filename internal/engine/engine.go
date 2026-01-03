package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"proofline/internal/config"
	"proofline/internal/domain"
	"proofline/internal/events"
	"proofline/internal/repo"
)

type Engine struct {
	DB     *sql.DB
	Repo   repo.Repo
	Events events.Writer
	Config *config.Config
	Now    func() time.Time
}

func New(db *sql.DB, cfg *config.Config) Engine {
	return Engine{
		DB:     db,
		Repo:   repo.Repo{DB: db},
		Events: events.Writer{DB: db},
		Config: cfg,
		Now:    time.Now,
	}
}

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// InitProject initializes a new project with migrations already run.
func (e Engine) InitProject(ctx context.Context, projectID, description, actorID string) (domain.Project, error) {
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback()

	p := domain.Project{
		ID:          projectID,
		Kind:        "software-project",
		Status:      "active",
		Description: description,
		CreatedAt:   e.now().UTC().Format(time.RFC3339),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id,kind,status,description,created_at) VALUES (?,?,?,?,?)`,
		p.ID, p.Kind, p.Status, nullable(p.Description), p.CreatedAt); err != nil {
		return domain.Project{}, fmt.Errorf("insert project: %w", err)
	}
	if err := e.Repo.UpsertProjectConfigTx(ctx, tx, p.ID, config.Default(p.ID)); err != nil {
		return domain.Project{}, fmt.Errorf("insert project config: %w", err)
	}
	if err := e.Events.Append(ctx, tx, "project.init", p.ID, "project", p.ID, actorID, events.EventPayload{"status": p.Status}); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Project{}, err
	}
	return p, nil
}

// TaskCreateOptions are parameters for creating a task.
type TaskCreateOptions struct {
	ID                string
	ProjectID         string
	IterationID       string
	ParentID          string
	Type              string
	Title             string
	Description       string
	DependsOn         []string
	AssigneeID        string
	PolicyPreset      string
	ValidationMode    string
	RequiredKinds     []string
	RequiredThreshold int
	ActorID           string
	PolicyOverride    bool
}

func (e Engine) CreateTask(ctx context.Context, opts TaskCreateOptions) (domain.Task, error) {
	if e.Config == nil {
		return domain.Task{}, errors.New("config not loaded")
	}
	if opts.Type == "" {
		opts.Type = "technical"
	}
	if opts.Title == "" {
		return domain.Task{}, errors.New("title is required")
	}
	if opts.ProjectID == "" {
		return domain.Task{}, errors.New("project is required")
	}
	if opts.ValidationMode == "threshold" && opts.RequiredThreshold == 0 {
		return domain.Task{}, errors.New("threshold required for validation-mode=threshold")
	}
	_, err := e.Repo.GetProject(ctx, opts.ProjectID)
	if err != nil {
		return domain.Task{}, err
	}
	if opts.IterationID != "" {
		it, err := e.Repo.GetIteration(ctx, opts.IterationID)
		if err != nil {
			return domain.Task{}, err
		}
		if it.ProjectID != opts.ProjectID {
			return domain.Task{}, fmt.Errorf("iteration %s not in project %s", opts.IterationID, opts.ProjectID)
		}
	}
	if opts.ParentID != "" {
		parent, err := e.Repo.GetTask(ctx, opts.ParentID)
		if err != nil {
			return domain.Task{}, err
		}
		if parent.ProjectID != opts.ProjectID {
			return domain.Task{}, errors.New("parent in different project")
		}
		if err := e.ensureNoCycle(ctx, opts.ParentID, opts.ID); err != nil {
			return domain.Task{}, err
		}
	}
	id := opts.ID
	now := e.now().UTC().Format(time.RFC3339)
	if id == "" {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(opts.ProjectID+"|"+opts.Title+"|"+now)).String()
	}
	reqJSON, err := marshalStringSlice(opts.RequiredKinds)
	if err != nil {
		return domain.Task{}, err
	}
	presetName := opts.PolicyPreset
	manualPolicy := opts.PolicyOverride
	if !manualPolicy {
		if presetName == "" {
			presetName = e.Config.Policies.Defaults.Task[opts.Type]
		}
		if presetName != "" {
			preset, ok := e.Config.Policies.Presets[presetName]
			if !ok {
				return domain.Task{}, fmt.Errorf("policy preset %s not found", presetName)
			}
			opts.ValidationMode = preset.Mode
			opts.RequiredKinds = preset.Require
			reqJSON, err = marshalStringSlice(preset.Require)
			if err != nil {
				return domain.Task{}, err
			}
			if preset.Mode == "threshold" {
				if preset.Threshold == nil {
					return domain.Task{}, fmt.Errorf("preset %s missing threshold", presetName)
				}
				opts.RequiredThreshold = *preset.Threshold
			}
		}
	}
	if opts.ValidationMode == "" {
		opts.ValidationMode = "none"
	}
	t := domain.Task{
		ID:                       id,
		ProjectID:                opts.ProjectID,
		IterationID:              optionalString(opts.IterationID),
		ParentID:                 optionalString(opts.ParentID),
		Type:                     opts.Type,
		Title:                    opts.Title,
		Description:              opts.Description,
		Status:                   "planned",
		AssigneeID:               optionalString(opts.AssigneeID),
		ValidationMode:           opts.ValidationMode,
		RequiredAttestationsJSON: reqJSON,
		RequiredThreshold:        optionalInt(opts.RequiredThreshold),
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()

	if err := e.Repo.InsertTask(ctx, tx, t); err != nil {
		return domain.Task{}, err
	}
	if len(opts.DependsOn) > 0 {
		if err := e.Repo.AddDependencies(ctx, tx, t.ID, opts.DependsOn); err != nil {
			return domain.Task{}, err
		}
	}
	if manualPolicy {
		if err := e.Events.Append(ctx, tx, "policy.override", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{
			"mode":      t.ValidationMode,
			"require":   opts.RequiredKinds,
			"threshold": opts.RequiredThreshold,
		}); err != nil {
			return domain.Task{}, err
		}
	} else if presetName != "" {
		if err := e.Events.Append(ctx, tx, "task.policy.applied", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{
			"preset_name": presetName,
			"mode":        t.ValidationMode,
			"require":     opts.RequiredKinds,
			"threshold":   opts.RequiredThreshold,
		}); err != nil {
			return domain.Task{}, err
		}
	}
	if err := e.Events.Append(ctx, tx, "task.created", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{"title": t.Title, "status": t.Status}); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	t.DependsOn = opts.DependsOn
	return t, nil
}

func marshalStringSlice(in []string) (*string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

func (e Engine) ensureNoCycle(ctx context.Context, parentID, childID string) error {
	// climb up parent chain to ensure no cycle
	cur := parentID
	for cur != "" {
		t, err := e.Repo.GetTask(ctx, cur)
		if err != nil {
			return err
		}
		if t.ParentID == nil {
			return nil
		}
		if *t.ParentID == childID {
			return errors.New("task hierarchy cycle detected")
		}
		cur = *t.ParentID
	}
	return nil
}

// TaskUpdateOptions encapsulates allowed updates.
type TaskUpdateOptions struct {
	ID             string
	Status         string
	Assign         *string
	AddDeps        []string
	RemoveDeps     []string
	SetParent      *string
	SetWorkProof   *string
	PolicyPreset   string
	ValidationMode string
	RequiredKinds  []string
	Threshold      *int
	ActorID        string
	Force          bool
	PolicyOverride bool
}

func (e Engine) UpdateTask(ctx context.Context, opts TaskUpdateOptions) (domain.Task, error) {
	if e.Config == nil {
		return domain.Task{}, errors.New("config not loaded")
	}
	t, err := e.Repo.GetTask(ctx, opts.ID)
	if err != nil {
		return t, err
	}
	oldPolicy := currentPolicy(t)
	original := t
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()

	if opts.SetParent != nil {
		if *opts.SetParent == "" {
			t.ParentID = nil
		} else {
			if err := e.ensureNoCycle(ctx, *opts.SetParent, t.ID); err != nil {
				return t, err
			}
			t.ParentID = opts.SetParent
		}
	}

	if opts.Assign != nil {
		if *opts.Assign == "" {
			t.AssigneeID = nil
		} else {
			t.AssigneeID = opts.Assign
		}
	}
	if t.ValidationMode == "" {
		t.ValidationMode = "none"
	}
	if opts.SetWorkProof != nil {
		if err := validateJSON(*opts.SetWorkProof); err != nil {
			return t, fmt.Errorf("work proof JSON: %w", err)
		}
		t.WorkProofJSON = opts.SetWorkProof
		if !opts.Force {
			if err := e.requireLeaseOrForce(ctx, tx, t.ID, opts.ActorID, opts.Force); err != nil {
				return t, err
			}
		}
	}
	if opts.PolicyPreset != "" {
		preset, ok := e.Config.Policies.Presets[opts.PolicyPreset]
		if !ok {
			return t, fmt.Errorf("policy preset %s not found", opts.PolicyPreset)
		}
		t.ValidationMode = preset.Mode
		reqJSON, err := marshalStringSlice(preset.Require)
		if err != nil {
			return t, err
		}
		t.RequiredAttestationsJSON = reqJSON
		t.RequiredThreshold = preset.Threshold
	}
	if opts.ValidationMode != "" {
		t.ValidationMode = opts.ValidationMode
	}
	if len(opts.RequiredKinds) > 0 || opts.PolicyOverride {
		reqJSON, err := marshalStringSlice(opts.RequiredKinds)
		if err != nil {
			return t, err
		}
		t.RequiredAttestationsJSON = reqJSON
		if opts.ValidationMode == "" && t.ValidationMode == "" {
			t.ValidationMode = "none"
		}
	}
	if opts.Threshold != nil {
		t.RequiredThreshold = opts.Threshold
	}
	if opts.Status != "" && opts.Status != t.Status {
		if !opts.Force {
			if err := e.requireLeaseOrForce(ctx, tx, t.ID, opts.ActorID, opts.Force); err != nil {
				return t, err
			}
		}
		if err := ensureTaskTransition(t.Status, opts.Status, opts.Force); err != nil {
			return t, err
		}
		if opts.Status == "done" && !opts.Force {
			if err := e.ensureDependenciesDone(ctx, tx, t.ID, t.ProjectID, opts.Force); err != nil {
				return t, err
			}
			if err := e.ensureSubtasksDone(ctx, tx, t.ID, opts.Force); err != nil {
				return t, err
			}
			ok, err := e.isTaskValidationSatisfied(ctx, tx, t, opts.ActorID)
			if err != nil {
				return t, err
			}
			if !ok {
				return t, errors.New("validation policy not satisfied")
			}
		}
		t.Status = opts.Status
		if opts.Status == "done" {
			now := e.now().UTC().Format(time.RFC3339)
			t.CompletedAt = &now
		}
	}
	t.UpdatedAt = e.now().UTC().Format(time.RFC3339)

	if len(opts.AddDeps) > 0 {
		if err := e.Repo.AddDependencies(ctx, tx, t.ID, opts.AddDeps); err != nil {
			return t, err
		}
	}
	if len(opts.RemoveDeps) > 0 {
		if err := e.Repo.RemoveDependencies(ctx, tx, t.ID, opts.RemoveDeps); err != nil {
			return t, err
		}
	}
	if err := e.Repo.UpdateTask(ctx, tx, t); err != nil {
		return t, err
	}
	newPolicy := currentPolicy(t)
	overrideEvent := opts.PolicyOverride || ((opts.ValidationMode != "" || len(opts.RequiredKinds) > 0 || opts.Threshold != nil) && opts.PolicyPreset == "")
	if opts.PolicyPreset != "" {
		if err := e.Events.Append(ctx, tx, "task.policy.updated", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{
			"preset_name":   opts.PolicyPreset,
			"old_mode":      oldPolicy.Mode,
			"old_require":   oldPolicy.Require,
			"old_threshold": oldPolicy.Threshold,
			"new_mode":      newPolicy.Mode,
			"new_require":   newPolicy.Require,
			"new_threshold": newPolicy.Threshold,
		}); err != nil {
			return t, err
		}
	} else if overrideEvent {
		if err := e.Events.Append(ctx, tx, "policy.override", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{
			"old_mode":      oldPolicy.Mode,
			"old_require":   oldPolicy.Require,
			"old_threshold": oldPolicy.Threshold,
			"new_mode":      newPolicy.Mode,
			"new_require":   newPolicy.Require,
			"new_threshold": newPolicy.Threshold,
		}); err != nil {
			return t, err
		}
	}
	if err := e.Events.Append(ctx, tx, "task.updated", t.ProjectID, "task", t.ID, opts.ActorID, events.EventPayload{
		"from_status": original.Status,
		"to_status":   t.Status,
	}); err != nil {
		return t, err
	}
	if err := tx.Commit(); err != nil {
		return t, err
	}
	t.DependsOn, _ = e.Repo.ListTaskDependencies(ctx, t.ID)
	return t, nil
}

func ensureTaskTransition(oldStatus, newStatus string, force bool) error {
	if force {
		return nil
	}
	switch oldStatus {
	case "planned":
		if newStatus == "in_progress" || newStatus == "canceled" {
			return nil
		}
	case "in_progress":
		if newStatus == "rejected" || newStatus == "canceled" || newStatus == "review" {
			return nil
		}
	case "review":
		if newStatus == "done" || newStatus == "rejected" {
			return nil
		}
	case "rejected":
		if newStatus == "planned" {
			return nil
		}
	}
	return fmt.Errorf("invalid task status transition %s -> %s", oldStatus, newStatus)
}

func validateJSON(in string) error {
	var tmp any
	if err := json.Unmarshal([]byte(in), &tmp); err != nil {
		return err
	}
	return nil
}

func (e Engine) requireLeaseOrForce(ctx context.Context, tx *sql.Tx, taskID, actorID string, force bool) error {
	if force {
		return nil
	}
	l, err := e.Repo.GetLease(ctx, taskID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errors.New("lease required; none exists")
		}
		return err
	}
	now := e.now()
	exp, _ := time.Parse(time.RFC3339, l.ExpiresAt)
	if now.After(exp) {
		return errors.New("lease expired; reacquire")
	}
	if l.OwnerID != actorID {
		return errors.New("lease owned by different actor")
	}
	return nil
}

// TaskDone sets work proof then tries to complete.
func (e Engine) TaskDone(ctx context.Context, taskID, workProofJSON, actorID string, force bool) (domain.Task, error) {
	if e.Config == nil {
		return domain.Task{}, errors.New("config not loaded")
	}
	if err := validateJSON(workProofJSON); err != nil {
		return domain.Task{}, fmt.Errorf("work-proof-json: %w", err)
	}
	t, err := e.Repo.GetTask(ctx, taskID)
	if err != nil {
		return t, err
	}
	if t.Status == "" {
		t.Status = "planned"
	}
	if t.ValidationMode == "" {
		t.ValidationMode = "none"
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()

	t.WorkProofJSON = &workProofJSON
	targetStatus := "done"
	if !force {
		// gating checks
		if err := e.requireLeaseOrForce(ctx, tx, t.ID, actorID, force); err != nil {
			return t, err
		}
		if err := e.ensureDependenciesDone(ctx, tx, t.ID, t.ProjectID, force); err != nil {
			return t, err
		}
		if err := e.ensureSubtasksDone(ctx, tx, t.ID, force); err != nil {
			return t, err
		}
		satisfied, err := e.isTaskValidationSatisfied(ctx, tx, t, actorID)
		if err != nil {
			return t, err
		}
		if !satisfied {
			targetStatus = "review"
		}
	}
	if err := ensureTaskTransition(t.Status, targetStatus, force); err != nil {
		return t, err
	}
	t.Status = targetStatus
	nowStr := e.now().UTC().Format(time.RFC3339)
	t.UpdatedAt = nowStr
	if t.Status == "done" {
		t.CompletedAt = &nowStr
	}
	if err := e.Repo.UpdateTask(ctx, tx, t); err != nil {
		return t, err
	}
	if err := e.Events.Append(ctx, tx, "task.done", t.ProjectID, "task", t.ID, actorID, events.EventPayload{"status": t.Status}); err != nil {
		return t, err
	}
	if err := tx.Commit(); err != nil {
		return t, err
	}
	t.DependsOn, _ = e.Repo.ListTaskDependencies(ctx, t.ID)
	return t, nil
}

func (e Engine) ensureDependenciesDone(ctx context.Context, tx *sql.Tx, taskID, projectID string, force bool) error {
	if force {
		return nil
	}
	deps, err := e.Repo.ListTaskDependencies(ctx, taskID)
	if err != nil {
		return err
	}
	for _, d := range deps {
		t, err := e.Repo.GetTask(ctx, d)
		if err != nil {
			return err
		}
		if t.ProjectID != projectID {
			return fmt.Errorf("dependency %s not in project", d)
		}
		if t.Status != "done" {
			return fmt.Errorf("dependency %s not done", d)
		}
	}
	return nil
}

func (e Engine) ensureSubtasksDone(ctx context.Context, tx *sql.Tx, taskID string, force bool) error {
	if force {
		return nil
	}
	children, err := e.Repo.ListChildren(ctx, taskID)
	if err != nil {
		return err
	}
	for _, c := range children {
		t, err := e.Repo.GetTask(ctx, c)
		if err != nil {
			return err
		}
		if t.Status != "done" {
			return fmt.Errorf("subtask %s not done", c)
		}
		if err := e.ensureSubtasksDone(ctx, tx, t.ID, force); err != nil {
			return err
		}
	}
	return nil
}

func (e Engine) isTaskValidationSatisfied(ctx context.Context, tx *sql.Tx, t domain.Task, actorID string) (bool, error) {
	if t.ValidationMode == "none" || t.RequiredAttestationsJSON == nil {
		return true, nil
	}
	var required []string
	if err := json.Unmarshal([]byte(*t.RequiredAttestationsJSON), &required); err != nil {
		return false, err
	}
	if len(required) == 0 {
		return true, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind FROM attestations WHERE entity_kind='task' AND entity_id=?`, t.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return false, err
		}
		for _, req := range required {
			if kind == req {
				found[kind] = true
			}
		}
	}
	switch t.ValidationMode {
	case "all":
		for _, req := range required {
			if !found[req] {
				return false, nil
			}
		}
		return true, nil
	case "any":
		return len(found) > 0, nil
	case "threshold":
		if t.RequiredThreshold == nil {
			return false, errors.New("threshold not set")
		}
		count := 0
		for _, req := range required {
			if found[req] {
				count++
			}
		}
		return count >= *t.RequiredThreshold, nil
	default:
		return true, nil
	}
}

// ClaimLease obtains a lease transactionally.
func (e Engine) ClaimLease(ctx context.Context, taskID, actorID string, leaseSeconds int) (domain.Lease, error) {
	if e.Config == nil {
		return domain.Lease{}, errors.New("config not loaded")
	}
	t, err := e.Repo.GetTask(ctx, taskID)
	if err != nil {
		return domain.Lease{}, err
	}
	_ = t // ensure task exists
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Lease{}, err
	}
	defer tx.Rollback()

	now := e.now().UTC()
	expires := now.Add(time.Duration(leaseSeconds) * time.Second)
	newLease := domain.Lease{
		TaskID:     taskID,
		OwnerID:    actorID,
		AcquiredAt: now.Format(time.RFC3339),
		ExpiresAt:  expires.Format(time.RFC3339),
	}
	existing, err := e.Repo.GetLease(ctx, taskID)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return domain.Lease{}, err
	}
	if err == nil {
		exp, _ := time.Parse(time.RFC3339, existing.ExpiresAt)
		if now.Before(exp) && existing.OwnerID != actorID {
			return domain.Lease{}, errors.New("lease already held")
		}
	}
	if err := e.Repo.UpsertLease(ctx, tx, newLease); err != nil {
		return domain.Lease{}, err
	}
	if err := e.Events.Append(ctx, tx, "lease.claimed", t.ProjectID, "lease", taskID, actorID, events.EventPayload{"expires_at": newLease.ExpiresAt}); err != nil {
		return domain.Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Lease{}, err
	}
	return newLease, nil
}

func (e Engine) ReleaseLease(ctx context.Context, taskID, actorID string) error {
	if e.Config == nil {
		return errors.New("config not loaded")
	}
	t, err := e.Repo.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := e.Repo.DeleteLease(ctx, tx, taskID); err != nil {
		return err
	}
	if err := e.Events.Append(ctx, tx, "lease.released", t.ProjectID, "lease", taskID, actorID, events.EventPayload{}); err != nil {
		return err
	}
	return tx.Commit()
}

func (e Engine) CreateIteration(ctx context.Context, it domain.Iteration, actorID string) (domain.Iteration, error) {
	if e.Config == nil {
		return it, errors.New("config not loaded")
	}
	if _, err := e.Repo.GetProject(ctx, it.ProjectID); err != nil {
		return it, err
	}
	if it.Status == "" {
		it.Status = "pending"
	}
	it.CreatedAt = e.now().UTC().Format(time.RFC3339)
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return it, err
	}
	defer tx.Rollback()
	if err := e.Repo.InsertIteration(ctx, it); err != nil {
		return it, err
	}
	if err := e.Events.Append(ctx, tx, "iteration.created", it.ProjectID, "iteration", it.ID, actorID, events.EventPayload{"status": it.Status}); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	return it, nil
}

func ensureIterationTransition(oldStatus, newStatus string, force bool) error {
	if force {
		return nil
	}
	switch oldStatus {
	case "pending":
		if newStatus == "running" {
			return nil
		}
	case "running":
		if newStatus == "delivered" || newStatus == "rejected" {
			return nil
		}
	case "delivered":
		if newStatus == "validated" || newStatus == "rejected" {
			return nil
		}
	}
	return fmt.Errorf("invalid iteration transition %s -> %s", oldStatus, newStatus)
}

func (e Engine) SetIterationStatus(ctx context.Context, id, status, actorID string, force bool) (domain.Iteration, error) {
	if e.Config == nil {
		return domain.Iteration{}, errors.New("config not loaded")
	}
	it, err := e.Repo.GetIteration(ctx, id)
	if err != nil {
		return it, err
	}
	if err := ensureIterationTransition(it.Status, status, force); err != nil {
		return it, err
	}
	requiredKind := ""
	if e.Config != nil {
		requiredKind = e.Config.Policies.Defaults.Iteration.Validation.Require
	}
	if status == "validated" && !force {
		if requiredKind != "" {
			ok, err := e.iterationValidated(ctx, id, requiredKind)
			if err != nil {
				return it, err
			}
			if !ok {
				return it, fmt.Errorf("attestation %s required for iteration validation", requiredKind)
			}
		}
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return it, err
	}
	defer tx.Rollback()
	if err := e.Repo.UpdateIterationStatus(ctx, tx, id, status); err != nil {
		return it, err
	}
	if status == "validated" {
		result := true
		if !force && requiredKind != "" {
			ok, err := e.iterationValidated(ctx, id, requiredKind)
			if err != nil {
				return it, err
			}
			result = ok
		}
		if err := e.Events.Append(ctx, tx, "iteration.validation.checked", it.ProjectID, "iteration", id, actorID, events.EventPayload{
			"required_kind": requiredKind,
			"result":        result,
		}); err != nil {
			return it, err
		}
	}
	if err := e.Events.Append(ctx, tx, "iteration.updated", it.ProjectID, "iteration", id, actorID, events.EventPayload{"from": it.Status, "to": status}); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	it.Status = status
	return it, nil
}

func (e Engine) iterationValidated(ctx context.Context, iterationID, kind string) (bool, error) {
	if kind == "" {
		return true, nil
	}
	rows, err := e.DB.QueryContext(ctx, `SELECT 1 FROM attestations WHERE entity_kind='iteration' AND entity_id=? AND kind=? LIMIT 1`, iterationID, kind)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), nil
}

func (e Engine) CreateDecision(ctx context.Context, d domain.Decision, actorID string) (domain.Decision, error) {
	if e.Config == nil {
		return d, errors.New("config not loaded")
	}
	if _, err := e.Repo.GetProject(ctx, d.ProjectID); err != nil {
		return d, err
	}
	d.CreatedAt = e.now().UTC().Format(time.RFC3339)
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if err := e.Repo.InsertDecision(ctx, d); err != nil {
		return d, err
	}
	if err := e.Events.Append(ctx, tx, "decision.created", d.ProjectID, "decision", d.ID, actorID, events.EventPayload{"title": d.Title}); err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	return d, nil
}

// AddAttestation inserts attestation and event.
func (e Engine) AddAttestation(ctx context.Context, att domain.Attestation, actorID string) (domain.Attestation, error) {
	if e.Config == nil {
		return att, errors.New("config not loaded")
	}
	if att.EntityKind == "" || att.EntityID == "" || att.Kind == "" {
		return att, errors.New("entity-kind, entity-id and kind required")
	}
	att.ID = uuid.New().String()
	if att.TS == "" {
		att.TS = e.now().UTC().Format(time.RFC3339)
	}
	if att.ProjectID == "" {
		return att, errors.New("project required")
	}
	if _, err := e.Repo.GetProject(ctx, att.ProjectID); err != nil {
		return att, err
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return att, err
	}
	defer tx.Rollback()
	if err := e.Repo.InsertAttestation(ctx, att); err != nil {
		return att, err
	}
	if err := e.Events.Append(ctx, tx, "attestation.added", att.ProjectID, "attestation", att.ID, actorID, events.EventPayload{"kind": att.Kind, "entity": att.EntityID}); err != nil {
		return att, err
	}
	if err := tx.Commit(); err != nil {
		return att, err
	}
	return att, nil
}

func (e Engine) ensureTaskPolicySatisfied(ctx context.Context, t domain.Task) (bool, error) {
	tx, err := e.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	return e.isTaskValidationSatisfied(ctx, tx, t, "")
}

// --- helpers ---

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

type policySnapshot struct {
	Mode      string
	Require   []string
	Threshold *int
}

func currentPolicy(t domain.Task) policySnapshot {
	var req []string
	if t.RequiredAttestationsJSON != nil && *t.RequiredAttestationsJSON != "" {
		_ = json.Unmarshal([]byte(*t.RequiredAttestationsJSON), &req)
	}
	return policySnapshot{
		Mode:      t.ValidationMode,
		Require:   req,
		Threshold: t.RequiredThreshold,
	}
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
