package app

import (
	"context"
	"errors"
	"fmt"

	"proofline/internal/config"
	"proofline/internal/repo"
)

// ResolveProjectAndConfig picks the active project and ensures a config is available in DB,
// seeding a default if missing. It prefers overrides, then config file, then single-project DB.
func ResolveProjectAndConfig(ctx context.Context, workspace, projectOverride string, r repo.Repo) (string, *config.Config, error) {
	fileCfg, err := config.LoadOptional(workspace)
	if err != nil {
		return "", nil, err
	}
	projectID := projectOverride
	if projectID == "" && fileCfg != nil {
		projectID = fileCfg.Project.ID
	}
	if projectID == "" {
		if p, err := r.SingleProject(ctx); err == nil {
			projectID = p.ID
		} else {
			return "", nil, fmt.Errorf("project not specified; use --project")
		}
	}
	if _, err := r.GetProject(ctx, projectID); err != nil {
		return "", nil, fmt.Errorf("project %s not found in db: %w", projectID, err)
	}
	cfg, err := r.GetProjectConfig(ctx, projectID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			seed := fileCfg
			if seed == nil || seed.Project.ID != projectID {
				seed = config.Default(projectID)
			}
			if err := r.UpsertProjectConfig(ctx, projectID, seed); err != nil {
				return "", nil, fmt.Errorf("seed project config: %w", err)
			}
			cfg = seed
		} else {
			return "", nil, err
		}
	}
	cfg.Project.ID = projectID
	return projectID, cfg, nil
}
