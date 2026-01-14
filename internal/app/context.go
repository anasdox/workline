package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"workline/internal/config"
	"workline/internal/domain"
	"workline/internal/repo"
)

// ResolveProjectAndConfig picks the active project and ensures a project + config exist in DB,
// seeding defaults if missing. It prefers overrides, then WORKLINE_DEFAULT_PROJECT.
// If the project does not exist, it is created on the fly.
func ResolveProjectAndConfig(ctx context.Context, workspace, projectOverride, actorID string, r repo.Repo) (string, *config.Config, error) {
	projectID := projectOverride
	if projectID == "" {
		projectID = os.Getenv("WORKLINE_DEFAULT_PROJECT")
	}
	if projectID == "" {
		return "", nil, fmt.Errorf("project not specified; use --project or set WORKLINE_DEFAULT_PROJECT (wl project use <id>)")
	}
	seedCfg := config.Default(projectID)

	if _, err := r.GetProject(ctx, projectID); err != nil {
		if !errors.Is(err, repo.ErrNotFound) {
			return "", nil, err
		}
		orgID := os.Getenv("WORKLINE_DEFAULT_ORG_ID")
		if orgID == "" {
			return "", nil, fmt.Errorf("org_id is required; set WORKLINE_DEFAULT_ORG_ID")
		}
		if err := createProject(ctx, r, projectID, orgID, seedCfg, actorID); err != nil {
			return "", nil, err
		}
	}
	cfg, err := r.GetProjectConfig(ctx, projectID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			if err := r.UpsertProjectConfig(ctx, projectID, seedCfg); err != nil {
				return "", nil, fmt.Errorf("seed project config: %w", err)
			}
			cfg = seedCfg
		} else {
			return "", nil, err
		}
	}
	cfg.Project.ID = projectID
	return projectID, cfg, nil
}

// createProject inserts a minimal project/org/rbac footprint using the seed config.
func createProject(ctx context.Context, r repo.Repo, projectID, orgID string, seedCfg *config.Config, actorID string) error {
	if seedCfg == nil {
		seedCfg = config.Default(projectID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p := domain.Project{
		ID:          projectID,
		OrgID:       orgID,
		Kind:        "software-project",
		Status:      "active",
		Description: "",
		CreatedAt:   now,
	}
	if err := r.EnsureOrg(ctx, tx, orgID, "Default Org", now); err != nil {
		return fmt.Errorf("ensure org: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id,org_id,kind,status,description,created_at) VALUES (?,?,?,?,?,?)`,
		p.ID, p.OrgID, p.Kind, p.Status, p.Description, p.CreatedAt); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	if err := r.UpsertProjectConfigTx(ctx, tx, projectID, seedCfg); err != nil {
		return fmt.Errorf("insert project config: %w", err)
	}
	if actorID == "" {
		actorID = "local-user"
	}
	if err := r.EnsureActor(ctx, tx, actorID, now); err != nil {
		return fmt.Errorf("ensure actor: %w", err)
	}
	if err := r.AssignOrgRole(ctx, tx, orgID, actorID, "owner"); err != nil {
		return fmt.Errorf("assign org role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
