package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	humachi "github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"proofline/internal/config"
	"proofline/internal/domain"
	"proofline/internal/engine"
	"proofline/internal/repo"
)

// Config for the HTTP API handler.
type Config struct {
	Engine   engine.Engine
	BasePath string
}

type apiErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty" jsonschema:"type=object,additionalProperties=true"`
}

type requestKey struct{}
type bodyBytesKey struct{}

// apiError models the required error envelope.
type apiError struct {
	status int
	Body   apiErrorBody `json:"error"`
}

func (e *apiError) GetStatus() int { return e.status }
func (e *apiError) Error() string  { return e.Body.Message }

// New returns an HTTP handler exposing the Proofline API.
func New(cfg Config) (http.Handler, error) {
	basePath := cfg.BasePath
	if basePath == "" {
		basePath = "/v0"
	}
	// Override Huma errors to use the requested envelope.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return newAPIError(status, "", msg, nil)
	}
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(msg), "validation") {
			// Schema/request validation errors should be 400 bad_request
			status = http.StatusBadRequest
		}
		var details map[string]any
		if len(errs) > 0 {
			details = map[string]any{"errors": errs}
		}
		return newAPIError(status, "", msg, details)
	}

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			ctx := context.WithValue(r.Context(), requestKey{}, r)
			ctx = context.WithValue(ctx, bodyBytesKey{}, bodyBytes)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	hcfg := huma.DefaultConfig("Proofline API", "0.1.0")
	hcfg.OpenAPIPath = "/openapi"
	hcfg.DocsPath = "" // custom Swagger UI below
	api := humachi.New(router, hcfg)
	group := huma.NewGroup(api, basePath)

	registerDocs(router, basePath)
	registerHealth(group)
	registerStatus(group, cfg.Engine)
	registerProjects(group, cfg.Engine)
	registerTasks(group, cfg.Engine)
	registerIterations(group, cfg.Engine)
	registerDecisions(group, cfg.Engine)
	registerAttestations(group, cfg.Engine)
	registerEvents(group, cfg.Engine)
	registerOpenAPI(router, api, basePath)

	return router, nil
}

func newAPIError(status int, code, message string, details map[string]any) huma.StatusError {
	if code == "" {
		code = defaultCodeForStatus(status)
	}
	return &apiError{
		status: status,
		Body: apiErrorBody{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

func handleError(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	if errors.Is(err, repo.ErrNotFound) {
		return newAPIError(http.StatusNotFound, "not_found", err.Error(), nil)
	}
	msg := err.Error()
	lowered := strings.ToLower(msg)
	switch {
	case strings.Contains(lowered, "lease") && (strings.Contains(lowered, "held") || strings.Contains(lowered, "owned")):
		return newAPIError(http.StatusConflict, "lease_conflict", msg, nil)
	case strings.Contains(lowered, "lease required"):
		return newAPIError(http.StatusConflict, "lease_conflict", msg, nil)
	case strings.Contains(lowered, "not done"),
		strings.Contains(lowered, "validation"),
		strings.Contains(lowered, "required for iteration validation"):
		return newAPIError(http.StatusUnprocessableEntity, "validation_failed", msg, nil)
	case strings.Contains(lowered, "invalid") || strings.Contains(lowered, "missing") || strings.Contains(lowered, "required"):
		return newAPIError(http.StatusBadRequest, "bad_request", msg, nil)
	default:
		return newAPIError(http.StatusInternalServerError, "internal_error", "internal error", map[string]any{"error": msg})
	}
}

func defaultCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusInternalServerError:
		return "internal_error"
	default:
		return strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
	}
}

func registerDocs(r chi.Router, basePath string) {
	r.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, swaggerHTML(basePath))
	})
}

func registerOpenAPI(r chi.Router, api huma.API, basePath string) {
	var spec []byte
	specPath := path.Join(basePath, "openapi.json")
	r.Get(specPath, func(w http.ResponseWriter, r *http.Request) {
		if spec == nil {
			spec, _ = json.Marshal(api.OpenAPI())
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(spec)
	})
}

func swaggerHTML(basePath string) string {
	specURL := path.Join("/", path.Join(basePath, "openapi.json"))
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width, initial-scale=1"/>
    <title>Proofline API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
    <script>
      window.onload = () => {
        SwaggerUIBundle({
          url: '%s',
          dom_id: '#swagger-ui'
        });
      };
    </script>
    <p style="padding: 1rem; font-family: sans-serif; color: #444;">
      Local, no-auth API for agents. Add auth before exposing beyond localhost.
    </p>
  </body>
</html>`, specURL)
}

func registerHealth(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body map[string]string `json:"body"`
	}, error) {
		return &struct {
			Body map[string]string `json:"body"`
		}{Body: map[string]string{"status": "ok"}}, nil
	})
}

func registerStatus(api huma.API, e engine.Engine) {
	type projectPath struct {
		ProjectID string `path:"project_id"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "status",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/status",
		Summary:     "Project status",
	}, func(ctx context.Context, input *projectPath) (*struct {
		Body map[string]any `json:"body"`
	}, error) {
		activeProject := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		p, err := e.Repo.GetProject(ctx, activeProject)
		if err != nil {
			return nil, handleError(err)
		}
		counts, err := e.Repo.CountTasksByStatus(ctx, p.ID)
		if err != nil {
			return nil, handleError(err)
		}
		running, err := e.Repo.LatestRunningIteration(ctx, p.ID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body map[string]any `json:"body"`
		}{Body: map[string]any{
			"project_id":  p.ID,
			"status":      p.Status,
			"iteration":   running,
			"task_counts": counts,
		}}, nil
	})
}

func registerProjects(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-project",
		Method:        http.MethodPost,
		Path:          "/projects",
		Summary:       "Create project",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ActorID string               `header:"X-Actor-Id" required:"true"`
		Body    CreateProjectRequest `json:"body"`
	}) (*struct {
		Body ProjectResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.ID == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "id is required", nil)
		}
		desc := ""
		if input.Body.Description != nil {
			desc = *input.Body.Description
		}
		p, err := e.InitProject(ctx, input.Body.ID, desc, actorOrDefault(input.ActorID))
		if err != nil {
			return nil, handleError(err)
		}
		if err := e.Repo.UpsertProjectConfig(ctx, p.ID, config.Default(p.ID)); err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body ProjectResponse `json:"body"`
		}{Body: projectResponse(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
		Summary:     "List projects",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []ProjectResponse `json:"body"`
	}, error) {
		items, err := e.Repo.ListProjects(ctx)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []ProjectResponse `json:"body"`
		}{Body: mapProjects(items)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}",
		Summary:     "Get project",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body ProjectResponse `json:"body"`
	}, error) {
		p, err := e.Repo.GetProject(ctx, input.ProjectID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body ProjectResponse `json:"body"`
		}{Body: projectResponse(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-project",
		Method:      http.MethodPatch,
		Path:        "/projects/{project_id}",
		Summary:     "Update project",
	}, func(ctx context.Context, input *struct {
		ActorID   string `header:"X-Actor-Id" required:"true"`
		ProjectID string `path:"project_id"`
		Body      struct {
			Status      string  `json:"status,omitempty"`
			Description *string `json:"description,omitempty"`
		} `json:"body"`
	}) (*struct {
		Body ProjectResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if err := e.Repo.UpdateProject(ctx, input.ProjectID, input.Body.Status, input.Body.Description); err != nil {
			return nil, handleError(err)
		}
		p, err := e.Repo.GetProject(ctx, input.ProjectID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body ProjectResponse `json:"body"`
		}{Body: projectResponse(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-project",
		Method:      http.MethodDelete,
		Path:        "/projects/{project_id}",
		Summary:     "Delete project",
	}, func(ctx context.Context, input *struct {
		ActorID   string `header:"X-Actor-Id" required:"true"`
		ProjectID string `path:"project_id"`
	}) (*struct{}, error) {
		if err := e.Repo.DeleteProject(ctx, input.ProjectID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-project-config",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/config",
		Summary:     "Get project config",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body ProjectConfigResponse `json:"body"`
	}, error) {
		cfg, err := e.Repo.GetProjectConfig(ctx, input.ProjectID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body ProjectConfigResponse `json:"body"`
		}{Body: configResponse(cfg)}, nil
	})
}

func registerTasks(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-task",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/tasks",
		Summary:       "Create task",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ActorID   string            `header:"X-Actor-Id" required:"true"`
		ProjectID string            `path:"project_id"`
		Body      CreateTaskRequest `json:"body"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.Title == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "title is required", nil)
		}
		if input.Body.Type == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "type is required", nil)
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		opts := engine.TaskCreateOptions{
			ProjectID:   projectID,
			Type:        input.Body.Type,
			Title:       input.Body.Title,
			ActorID:     actorOrDefault(input.ActorID),
			Description: stringOrEmpty(input.Body.Description),
			DependsOn:   input.Body.DependsOn,
		}
		if input.Body.ID != nil {
			opts.ID = *input.Body.ID
		}
		if input.Body.IterationID != nil {
			opts.IterationID = *input.Body.IterationID
		}
		if input.Body.ParentID != nil {
			opts.ParentID = *input.Body.ParentID
		}
		if input.Body.AssigneeID != nil {
			opts.AssigneeID = *input.Body.AssigneeID
		}
		if input.Body.Policy != nil {
			opts.PolicyPreset = input.Body.Policy.Preset
		}
		if input.Body.Validation != nil {
			opts.PolicyOverride = true
			opts.ValidationMode = input.Body.Validation.Mode
			opts.RequiredKinds = input.Body.Validation.Require
			if input.Body.Validation.Threshold != nil {
				opts.RequiredThreshold = *input.Body.Validation.Threshold
			}
			if opts.ValidationMode == "threshold" && input.Body.Validation.Threshold == nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "threshold required for validation.mode=threshold", nil)
			}
		}
		if input.Body.WorkProof != nil {
			b, err := json.Marshal(input.Body.WorkProof)
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_proof", map[string]any{"error": err.Error()})
			}
			asStr := string(b)
			opts.WorkProofJSON = &asStr
		}
		t, err := e.CreateTask(ctx, opts)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body TaskResponse `json:"body"`
		}{Body: taskResponse(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-tasks",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks",
		Summary:     "List tasks",
	}, func(ctx context.Context, input *struct {
		ProjectID   string `path:"project_id"`
		Status      string `query:"status"`
		IterationID string `query:"iteration_id"`
		ParentID    string `query:"parent_id"`
		AssigneeID  string `query:"assignee_id"`
		Limit       int    `query:"limit" default:"50"`
		Cursor      string `query:"cursor"`
	}) (*struct {
		Body paginatedTasks `json:"body"`
	}, error) {
		limit := normalizeLimit(input.Limit)
		cursorCreated, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		filter := repo.TaskFilters{
			ProjectID:       projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID),
			Status:          input.Status,
			Iteration:       input.IterationID,
			Parent:          input.ParentID,
			AssigneeID:      input.AssigneeID,
			Limit:           limit + 1,
			CursorCreatedAt: cursorCreated,
			CursorID:        cursorID,
		}
		tasks, err := e.Repo.ListTasks(ctx, filter)
		if err != nil {
			return nil, handleError(err)
		}
		resp := paginatedTasks{Items: []TaskResponse{}}
		if len(tasks) > limit {
			resp.NextCursor = composeCursor(tasks[limit].CreatedAt, tasks[limit].ID)
			tasks = tasks[:limit]
		}
		resp.Items = mapTasks(tasks)
		return &struct {
			Body paginatedTasks `json:"body"`
		}{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-task",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks/{id}",
		Summary:     "Get task",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		t, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		return &struct {
			Body TaskResponse `json:"body"`
		}{Body: taskResponse(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-task",
		Method:      http.MethodPatch,
		Path:        "/projects/{project_id}/tasks/{id}",
		Summary:     "Update task",
	}, func(ctx context.Context, input *struct {
		ActorID   string            `header:"X-Actor-Id" required:"true"`
		ProjectID string            `path:"project_id"`
		ID        string            `path:"id"`
		Body      UpdateTaskRequest `json:"body"`
		Force     bool              `query:"force"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		bodyMap := rawBodyMap(ctx)
		opts := engine.TaskUpdateOptions{
			ID:      input.ID,
			ActorID: actorOrDefault(input.ActorID),
			Force:   input.Force,
		}
		if input.Body.Status != nil {
			opts.Status = *input.Body.Status
		}
		if _, ok := bodyMap["assignee_id"]; ok {
			opts.AssignProvided = true
			opts.Assign = input.Body.AssigneeID
		}
		if _, ok := bodyMap["parent_id"]; ok {
			opts.ParentProvided = true
			opts.SetParent = input.Body.ParentID
		}
		if _, ok := bodyMap["work_proof"]; ok {
			opts.WorkProofSet = true
			if input.Body.WorkProof == nil {
				opts.ClearWorkProof = true
			} else {
				data, err := json.Marshal(input.Body.WorkProof)
				if err != nil {
					return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_proof", map[string]any{"error": err.Error()})
				}
				asStr := string(data)
				opts.SetWorkProof = &asStr
			}
		}
		opts.AddDeps = input.Body.AddDependsOn
		opts.RemoveDeps = input.Body.RemoveDependsOn
		if rawValidation, ok := bodyMap["validation"]; ok {
			opts.PolicyOverride = true
			var validationMap map[string]json.RawMessage
			_ = json.Unmarshal(rawValidation, &validationMap)
			if input.Body.Validation != nil {
				if input.Body.Validation.Mode != nil {
					opts.ValidationModeSet = true
					opts.ValidationMode = *input.Body.Validation.Mode
				}
				if input.Body.Validation.Require != nil {
					opts.RequiredKindsSet = true
					opts.RequiredKinds = input.Body.Validation.Require
				}
				if _, present := validationMap["threshold"]; present {
					opts.ThresholdSet = true
					opts.Threshold = input.Body.Validation.Threshold
				}
				if opts.ValidationMode == "threshold" && opts.Threshold == nil {
					return nil, newAPIError(http.StatusBadRequest, "bad_request", "threshold required for validation.mode=threshold", nil)
				}
			} else {
				opts.ValidationModeSet = true
				opts.ValidationMode = "none"
				opts.RequiredKindsSet = true
				opts.RequiredKinds = nil
				if _, present := validationMap["threshold"]; present {
					opts.ThresholdSet = true
				}
			}
		}
		t, err := e.UpdateTask(ctx, opts)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		return &struct {
			Body TaskResponse `json:"body"`
		}{Body: taskResponse(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "complete-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/done",
		Summary:     "Complete task",
	}, func(ctx context.Context, input *struct {
		ActorID   string              `header:"X-Actor-Id" required:"true"`
		ProjectID string              `path:"project_id"`
		ID        string              `path:"id"`
		Body      CompleteTaskRequest `json:"body"`
		Force     bool                `query:"force"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.WorkProof == nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "work_proof is required", nil)
		}
		data, err := json.Marshal(input.Body.WorkProof)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_proof", map[string]any{"error": err.Error()})
		}
		workProof := string(data)
		t, err := e.TaskDone(ctx, input.ID, workProof, actorOrDefault(input.ActorID), input.Force)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		return &struct {
			Body TaskResponse `json:"body"`
		}{Body: taskResponse(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "claim-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/claim",
		Summary:     "Claim task lease",
	}, func(ctx context.Context, input *struct {
		ActorID      string `header:"X-Actor-Id" required:"true"`
		ProjectID    string `path:"project_id"`
		ID           string `path:"id"`
		LeaseSeconds int    `query:"lease_seconds" default:"900"`
	}) (*struct {
		Body LeaseResponse `json:"body"`
	}, error) {
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		lease, err := e.ClaimLease(ctx, input.ID, actorOrDefault(input.ActorID), input.LeaseSeconds)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body LeaseResponse `json:"body"`
		}{Body: LeaseResponse(lease)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "release-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/release",
		Summary:     "Release task lease",
	}, func(ctx context.Context, input *struct {
		ActorID   string `header:"X-Actor-Id" required:"true"`
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct{}, error) {
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		if err := e.ReleaseLease(ctx, input.ID, actorOrDefault(input.ActorID)); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	type treeInput struct {
		ProjectID string `path:"project_id"`
		Iteration string `query:"iteration_id"`
		Status    string `query:"status"`
	}
	type treeNode struct {
		Task     TaskResponse `json:"task"`
		Children []treeNode   `json:"children,omitempty"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "task-tree",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks/tree",
		Summary:     "Task tree",
	}, func(ctx context.Context, input *treeInput) (*struct {
		Body []treeNode `json:"body"`
	}, error) {
		tasks, err := e.Repo.ListTasks(ctx, repo.TaskFilters{ProjectID: projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID), Iteration: input.Iteration, Status: input.Status})
		if err != nil {
			return nil, handleError(err)
		}
		children := map[string][]domain.Task{}
		var roots []domain.Task
		for _, t := range tasks {
			if t.ParentID != nil {
				children[*t.ParentID] = append(children[*t.ParentID], t)
			} else {
				roots = append(roots, t)
			}
		}
		var build func(domain.Task) treeNode
		build = func(t domain.Task) treeNode {
			var kid []treeNode
			for _, c := range children[t.ID] {
				kid = append(kid, build(c))
			}
			return treeNode{Task: taskResponse(t), Children: kid}
		}
		var res []treeNode
		for _, r := range roots {
			res = append(res, build(r))
		}
		return &struct {
			Body []treeNode `json:"body"`
		}{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "task-validation-status",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks/{id}/validation",
		Summary:     "Task validation status",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct {
		Body ValidationStatusResponse `json:"body"`
	}, error) {
		t, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		status, err := taskValidationStatus(ctx, e.Repo, t)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body ValidationStatusResponse `json:"body"`
		}{Body: status}, nil
	})
}

func registerIterations(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-iteration",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/iterations",
		Summary:       "Create iteration",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ActorID   string                 `header:"X-Actor-Id" required:"true"`
		ProjectID string                 `path:"project_id"`
		Body      CreateIterationRequest `json:"body"`
	}) (*struct {
		Body IterationResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.ID == "" || input.Body.Goal == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "id and goal are required", nil)
		}
		bodyProject := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		it := domain.Iteration{
			ID:        input.Body.ID,
			ProjectID: bodyProject,
			Goal:      input.Body.Goal,
		}
		res, err := e.CreateIteration(ctx, it, actorOrDefault(input.ActorID))
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body IterationResponse `json:"body"`
		}{Body: iterationResponse(res)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-iterations",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/iterations",
		Summary:     "List iterations",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		Limit     int    `query:"limit" default:"50"`
		Cursor    string `query:"cursor"`
	}) (*struct {
		Body paginatedIterations `json:"body"`
	}, error) {
		limit := normalizeLimit(input.Limit)
		cursorCreated, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		items, err := e.Repo.ListIterationsWithCursor(ctx, projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID), limit+1, cursorCreated, cursorID)
		if err != nil {
			return nil, handleError(err)
		}
		resp := paginatedIterations{Items: []IterationResponse{}}
		if len(items) > limit {
			resp.NextCursor = composeCursor(items[limit].CreatedAt, items[limit].ID)
			items = items[:limit]
		}
		for _, it := range items {
			resp.Items = append(resp.Items, iterationResponse(it))
		}
		return &struct {
			Body paginatedIterations `json:"body"`
		}{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "set-iteration-status",
		Method:      http.MethodPatch,
		Path:        "/projects/{project_id}/iterations/{id}/status",
		Summary:     "Update iteration status",
	}, func(ctx context.Context, input *struct {
		ActorID   string                    `header:"X-Actor-Id" required:"true"`
		ProjectID string                    `path:"project_id"`
		ID        string                    `path:"id"`
		Body      SetIterationStatusRequest `json:"body"`
		Force     bool                      `query:"force"`
	}) (*struct {
		Body IterationResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.Status == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "status is required", nil)
		}
		it, err := e.SetIterationStatus(ctx, input.ID, input.Body.Status, actorOrDefault(input.ActorID), input.Force)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, it.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "iteration not found in project", nil)
		}
		return &struct {
			Body IterationResponse `json:"body"`
		}{Body: iterationResponse(it)}, nil
	})
}

func registerDecisions(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-decision",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/decisions",
		Summary:       "Create decision",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ActorID   string                `header:"X-Actor-Id" required:"true"`
		ProjectID string                `path:"project_id"`
		Body      CreateDecisionRequest `json:"body"`
	}) (*struct {
		Body DecisionResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.ID == "" || input.Body.Title == "" || input.Body.Decision == "" || input.Body.DeciderID == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "id, title, decision, and decider_id are required", nil)
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		d := domain.Decision{
			ID:        input.Body.ID,
			ProjectID: projectID,
			Title:     input.Body.Title,
			Decision:  input.Body.Decision,
			DeciderID: input.Body.DeciderID,
		}
		if input.Body.Context != nil {
			if data, err := json.Marshal(input.Body.Context); err == nil {
				d.ContextJSON = string(data)
			} else {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid context", map[string]any{"error": err.Error()})
			}
		}
		if len(input.Body.Rationale) > 0 {
			d.RationaleJSON = toJSONArray(input.Body.Rationale)
		}
		if len(input.Body.Alternatives) > 0 {
			d.AlternativesJSON = toJSONArray(input.Body.Alternatives)
		}
		res, err := e.CreateDecision(ctx, d, actorOrDefault(input.ActorID))
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body DecisionResponse `json:"body"`
		}{Body: decisionResponse(res)}, nil
	})
}

func registerAttestations(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "add-attestation",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/attestations",
		Summary:       "Add attestation",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ActorID   string                   `header:"X-Actor-Id" required:"true"`
		ProjectID string                   `path:"project_id"`
		Body      CreateAttestationRequest `json:"body"`
	}) (*struct {
		Body AttestationResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.EntityKind == "" || input.Body.EntityID == "" || input.Body.Kind == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "entity_kind, entity_id and kind are required", nil)
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		payload := ""
		if input.Body.Payload != nil {
			b, err := json.Marshal(input.Body.Payload)
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid payload", map[string]any{"error": err.Error()})
			}
			payload = string(b)
		}
		att := domain.Attestation{
			ID:          strPtrValue(input.Body.ID),
			ProjectID:   projectID,
			EntityKind:  input.Body.EntityKind,
			EntityID:    input.Body.EntityID,
			Kind:        input.Body.Kind,
			PayloadJSON: payload,
		}
		if input.Body.TS != nil {
			att.TS = *input.Body.TS
		}
		res, err := e.AddAttestation(ctx, att, actorOrDefault(input.ActorID))
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body AttestationResponse `json:"body"`
		}{Body: attestationResponse(res)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-attestations",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/attestations",
		Summary:     "List attestations",
	}, func(ctx context.Context, input *struct {
		ProjectID  string `path:"project_id"`
		EntityKind string `query:"entity_kind"`
		EntityID   string `query:"entity_id"`
		Kind       string `query:"kind"`
		Limit      int    `query:"limit" default:"50"`
		Cursor     string `query:"cursor"`
	}) (*struct {
		Body paginatedAttestations `json:"body"`
	}, error) {
		limit := normalizeLimit(input.Limit)
		cursorTS, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		f := repo.AttestationFilters{
			ProjectID:  projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID),
			EntityKind: input.EntityKind,
			EntityID:   input.EntityID,
			Kind:       input.Kind,
			Limit:      limit + 1,
			CursorTS:   cursorTS,
			CursorID:   cursorID,
		}
		items, err := e.Repo.ListAttestations(ctx, f)
		if err != nil {
			return nil, handleError(err)
		}
		resp := paginatedAttestations{Items: []AttestationResponse{}}
		if len(items) > limit {
			resp.NextCursor = composeCursor(items[limit].TS, items[limit].ID)
			items = items[:limit]
		}
		for _, att := range items {
			resp.Items = append(resp.Items, attestationResponse(att))
		}
		return &struct {
			Body paginatedAttestations `json:"body"`
		}{Body: resp}, nil
	})
}

func registerEvents(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "list-events",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/events",
		Summary:     "List recent events",
	}, func(ctx context.Context, input *struct {
		ProjectID  string `path:"project_id"`
		Type       string `query:"type"`
		EntityKind string `query:"entity_kind"`
		EntityID   string `query:"entity_id"`
		Limit      int    `query:"limit" default:"50"`
		Cursor     string `query:"cursor"`
	}) (*struct {
		Body paginatedEvents `json:"body"`
	}, error) {
		limit := normalizeLimit(input.Limit)
		var cursorID int64
		if input.Cursor != "" {
			parsed, err := strconv.ParseInt(input.Cursor, 10, 64)
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
			}
			cursorID = parsed
		}
		items, err := e.Repo.LatestEventsFrom(ctx, limit+1, cursorID, projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID), input.Type, input.EntityKind, input.EntityID)
		if err != nil {
			return nil, handleError(err)
		}
		resp := paginatedEvents{Items: []EventResponse{}}
		if len(items) > limit {
			resp.NextCursor = fmt.Sprintf("%d", items[limit].ID)
			items = items[:limit]
		}
		for _, evt := range items {
			resp.Items = append(resp.Items, eventResponse(evt))
		}
		return &struct {
			Body paginatedEvents `json:"body"`
		}{Body: resp}, nil
	})
}

func bodyBytes(ctx context.Context) []byte {
	if buf, ok := ctx.Value(bodyBytesKey{}).([]byte); ok {
		return buf
	}
	req, ok := ctx.Value(requestKey{}).(*http.Request)
	if !ok || req == nil {
		return nil
	}
	data, _ := io.ReadAll(req.Body)
	return data
}

func rawBodyMap(ctx context.Context) map[string]json.RawMessage {
	data := bodyBytes(ctx)
	if len(data) == 0 {
		return map[string]json.RawMessage{}
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(data, &outer); err != nil {
		return map[string]json.RawMessage{}
	}
	if inner, ok := outer["body"]; ok {
		var innerMap map[string]json.RawMessage
		if err := json.Unmarshal(inner, &innerMap); err == nil {
			return innerMap
		}
	}
	return outer
}

func normalizeLimit(in int) int {
	if in <= 0 {
		return 50
	}
	if in > 200 {
		return 200
	}
	return in
}

func parseCompositeCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	parts := strings.SplitN(cursor, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid cursor")
	}
	return parts[0], parts[1], nil
}

func composeCursor(ts, id string) string {
	if ts == "" || id == "" {
		return ""
	}
	return ts + "|" + id
}

func mapProjects(items []domain.Project) []ProjectResponse {
	res := make([]ProjectResponse, 0, len(items))
	for _, p := range items {
		res = append(res, projectResponse(p))
	}
	return res
}

func mapTasks(items []domain.Task) []TaskResponse {
	res := make([]TaskResponse, 0, len(items))
	for _, t := range items {
		res = append(res, taskResponse(t))
	}
	return res
}

func stringOrEmpty(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func strPtrValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func toJSONArray(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
}

func taskValidationStatus(ctx context.Context, r repo.Repo, t domain.Task) (ValidationStatusResponse, error) {
	mode := defaultMode(t.ValidationMode)
	required := decodeStringSlice(t.RequiredAttestationsJSON)
	resp := ValidationStatusResponse{
		Mode:      mode,
		Required:  nonNilSlice(required),
		Threshold: t.RequiredThreshold,
		Present:   []string{},
		Missing:   []string{},
	}
	if mode == "none" || len(required) == 0 {
		resp.Satisfied = true
		return resp, nil
	}
	atts, err := r.ListAttestations(ctx, repo.AttestationFilters{
		EntityKind: "task",
		EntityID:   t.ID,
		ProjectID:  t.ProjectID,
	})
	if err != nil {
		return resp, err
	}
	found := map[string]bool{}
	for _, att := range atts {
		found[att.Kind] = true
	}
	for _, req := range required {
		if found[req] {
			resp.Present = append(resp.Present, req)
		} else {
			resp.Missing = append(resp.Missing, req)
		}
	}
	switch mode {
	case "all":
		resp.Satisfied = len(resp.Missing) == 0
	case "any":
		resp.Satisfied = len(resp.Present) > 0
	case "threshold":
		if resp.Threshold == nil {
			resp.Satisfied = false
		} else {
			resp.Satisfied = len(resp.Present) >= *resp.Threshold
		}
	default:
		resp.Satisfied = true
	}
	return resp, nil
}

func actorOrDefault(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "local-user"
	}
	return id
}

func projectFromPathOrHeader(ctx context.Context, pathProjectID, fallback string) string {
	if pathProjectID != "" {
		return pathProjectID
	}
	return projectFromHeader(ctx, fallback)
}

func projectMatches(expected, actual string) bool {
	if expected == "" {
		return true
	}
	return expected == actual
}

func projectFromHeader(ctx context.Context, fallback string) string {
	if h, ok := ctx.(interface{ Header(string) string }); ok {
		if v := strings.TrimSpace(h.Header("X-Project-Id")); v != "" {
			return v
		}
	}
	if req, ok := ctx.Value(requestKey{}).(*http.Request); ok && req != nil {
		if v := strings.TrimSpace(req.Header.Get("X-Project-Id")); v != "" {
			return v
		}
	}
	return fallback
}
