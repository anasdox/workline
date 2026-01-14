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

	"workline/internal/config"
	"workline/internal/domain"
	"workline/internal/engine"
	"workline/internal/engine/auth"
	"workline/internal/repo"
)

// Config for the HTTP API handler.
type Config struct {
	Engine   engine.Engine
	BasePath string
	Auth     AuthConfig
}

type apiErrorBody struct {
	Code    string         `json:"code" example:"forbidden_attestation_kind"`
	Message string         `json:"message" example:"actor cannot attest to this kind"`
	Details map[string]any `json:"details,omitempty" jsonschema:"type=object,additionalProperties=true" example:"{\"kind\":\"security.ok\"}"`
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

// New returns an HTTP handler exposing the Workline API.
func New(cfg Config) (http.Handler, error) {
	basePath := cfg.BasePath
	if basePath == "" {
		basePath = "/v0"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	huma.DefaultArrayNullable = false
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
	router.Use(newAuthMiddleware(basePath, cfg.Auth, cfg.Engine.Repo))
	hcfg := huma.DefaultConfig("Workline API", "0.1.1")
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
	registerRBAC(group, cfg.Engine)
	registerMe(group, cfg.Engine)
	registerDevAuth(group, cfg.Engine, cfg.Auth)
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
	var fe auth.ForbiddenError
	if errors.As(err, &fe) {
		return newAPIError(http.StatusForbidden, "forbidden", err.Error(), map[string]any{"permission": fe.Permission})
	}
	var ae auth.ForbiddenAttestationError
	if errors.As(err, &ae) {
		return newAPIError(http.StatusForbidden, "forbidden_attestation_kind", err.Error(), map[string]any{"kind": ae.Kind})
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
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusInternalServerError:
		return "internal_error"
	default:
		return strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
	}
}

func hasPermission(perms []string, perm string) bool {
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

func requirePermission(ctx context.Context, e engine.Engine, projectID, perm string) error {
	principal, authErr := principalFromRequest(ctx)
	if authErr != nil {
		return authErr
	}
	if hasPermission(principal.Permissions, perm) {
		return nil
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ok, err := e.Auth.ActorHasPermission(ctx, tx, projectID, principal.ActorID, perm)
	if err != nil {
		return err
	}
	if !ok {
		return auth.ForbiddenError{Permission: perm}
	}
	return nil
}

func requireGlobalPermission(ctx context.Context, e engine.Engine, perm string) error {
	principal, authErr := principalFromRequest(ctx)
	if authErr != nil {
		return authErr
	}
	if hasPermission(principal.Permissions, perm) {
		return nil
	}
	if e.Config == nil {
		return auth.ForbiddenError{Permission: perm}
	}
	return requirePermission(ctx, e, e.Config.Project.ID, perm)
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
			oas := api.OpenAPI()
			ensureDefaultErrorResponses(oas)
			applyAuthSecurity(oas, basePath)
			spec, _ = json.Marshal(oas)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(spec)
	})
}

func ensureDefaultErrorResponses(oas *huma.OpenAPI) {
	if oas == nil || oas.Paths == nil {
		return
	}
	for _, item := range oas.Paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace,
		} {
			if op == nil {
				continue
			}
			if op.Responses == nil {
				op.Responses = map[string]*huma.Response{}
			}
			op.Responses["default"] = &huma.Response{
				Description: "Error",
				Content: map[string]*huma.MediaType{
					"application/json": {
						Schema: &huma.Schema{Ref: "#/components/schemas/ApiError"},
					},
				},
			}
		}
	}
}

func applyAuthSecurity(oas *huma.OpenAPI, basePath string) {
	if oas == nil {
		return
	}
	if oas.Components == nil {
		oas.Components = &huma.Components{}
	}
	if oas.Components.SecuritySchemes == nil {
		oas.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}
	oas.Components.SecuritySchemes["bearerAuth"] = &huma.SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "JWT",
	}
	oas.Components.SecuritySchemes["apiKeyAuth"] = &huma.SecurityScheme{
		Type: "apiKey",
		In:   "header",
		Name: "X-Api-Key",
	}
	security := []map[string][]string{
		{"bearerAuth": {}},
		{"apiKeyAuth": {}},
	}
	oas.Security = security
	healthPath := path.Join(basePath, "health")
	devLoginPath := path.Join(basePath, "auth/dev/login")
	if !strings.HasPrefix(healthPath, "/") {
		healthPath = "/" + healthPath
	}
	if !strings.HasPrefix(devLoginPath, "/") {
		devLoginPath = "/" + devLoginPath
	}
	for route, item := range oas.Paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace,
		} {
			if op == nil {
				continue
			}
			if route == healthPath {
				op.Security = []map[string][]string{}
				continue
			}
			if route == devLoginPath {
				op.Security = []map[string][]string{}
				continue
			}
			op.Security = security
		}
	}
}

func swaggerHTML(basePath string) string {
	specURL := path.Join("/", path.Join(basePath, "openapi.json"))
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width, initial-scale=1"/>
    <title>Workline API Docs</title>
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
      Authenticate with Authorization: Bearer &lt;token&gt; or X-Api-Key.
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
		if err := requirePermission(ctx, e, activeProject, "project.status.read"); err != nil {
			return nil, handleError(err)
		}
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		Body CreateProjectRequest `json:"body"`
	}) (*struct {
		Body ProjectResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.ID == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "id is required", nil)
		}
		if input.Body.OrgID == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "org_id is required", nil)
		}
		if err := requireGlobalPermission(ctx, e, "project.create"); err != nil {
			return nil, handleError(err)
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		desc := ""
		if input.Body.Description != nil {
			desc = *input.Body.Description
		}
		p, err := e.InitProject(ctx, input.Body.ID, input.Body.OrgID, desc, actorID)
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
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []ProjectResponse `json:"body"`
	}, error) {
		if err := requireGlobalPermission(ctx, e, "project.list"); err != nil {
			return nil, handleError(err)
		}
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
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body ProjectResponse `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "project.read"); err != nil {
			return nil, handleError(err)
		}
		p, err := e.Repo.GetProject(ctx, projectID)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, input *struct {
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
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "project.update"); err != nil {
			return nil, handleError(err)
		}
		if _, authErr := actorIDFromContext(ctx); authErr != nil {
			return nil, authErr
		}
		if err := e.Repo.UpdateProject(ctx, projectID, input.Body.Status, input.Body.Description); err != nil {
			return nil, handleError(err)
		}
		p, err := e.Repo.GetProject(ctx, projectID)
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
		Errors: []int{
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct{}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "project.delete"); err != nil {
			return nil, handleError(err)
		}
		if _, authErr := actorIDFromContext(ctx); authErr != nil {
			return nil, authErr
		}
		if err := e.Repo.DeleteProject(ctx, projectID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-project-config",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/config",
		Summary:     "Get project config",
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body ProjectConfigResponse `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "project.config.read"); err != nil {
			return nil, handleError(err)
		}
		cfg, err := e.Repo.GetProjectConfig(ctx, projectID)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusUnprocessableEntity,
			http.StatusConflict,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string            `path:"project_id"`
		Body      CreateTaskRequest `json:"body"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		bodyMap := rawBodyMap(ctx)
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		if input.Body.Title == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "title is required", nil)
		}
		if input.Body.Type == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "type is required", nil)
		}
		if isNullRaw(bodyMap["depends_on"]) {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "depends_on must be array", map[string]any{"field": "depends_on", "reason": "must be array"})
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		opts := engine.TaskCreateOptions{
			ProjectID:   projectID,
			Type:        input.Body.Type,
			Title:       input.Body.Title,
			ActorID:     actorID,
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
		if input.Body.Priority != nil {
			opts.Priority = input.Body.Priority
		}
		if input.Body.Policy != nil {
			opts.PolicyPreset = input.Body.Policy.Preset
		} else if rawPolicy, ok := bodyMap["policy"]; ok {
			var policy TaskPolicyRequest
			if err := json.Unmarshal(rawPolicy, &policy); err == nil && policy.Preset != "" {
				opts.PolicyPreset = policy.Preset
			}
		}
		if rawValidation, ok := bodyMap["validation"]; ok {
			var validationMap map[string]json.RawMessage
			if len(rawValidation) > 0 {
				_ = json.Unmarshal(rawValidation, &validationMap)
				if isNullRaw(validationMap["require"]) {
					return nil, newAPIError(http.StatusBadRequest, "bad_request", "validation.require must be array", map[string]any{"field": "validation.require", "reason": "must be array"})
				}
			}
			if input.Body.Validation != nil {
				opts.PolicyOverride = true
				opts.RequiredKinds = input.Body.Validation.Require
			}
		}
		if input.Body.WorkOutcomes != nil {
			b, err := json.Marshal(input.Body.WorkOutcomes)
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_outcomes", map[string]any{"error": err.Error()})
			}
			asStr := string(b)
			opts.WorkOutcomesJSON = &asStr
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
		Errors:      []int{http.StatusBadRequest},
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
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "task.list"); err != nil {
			return nil, handleError(err)
		}
		limit := normalizeLimit(input.Limit)
		cursorCreated, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		filter := repo.TaskFilters{
			ProjectID:       projectID,
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
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct {
		Body TaskResponse `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "task.read"); err != nil {
			return nil, handleError(err)
		}
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
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
		if input.Body.Validation == nil {
			var parsed UpdateTaskRequest
			if err := json.Unmarshal(bodyBytes(ctx), &parsed); err == nil && parsed.Validation != nil {
				input.Body.Validation = parsed.Validation
			}
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		opts := engine.TaskUpdateOptions{
			ID:      input.ID,
			ActorID: actorID,
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
		if rawPriority, ok := bodyMap["priority"]; ok {
			opts.PriorityProvided = true
			if isNullRaw(rawPriority) {
				opts.ClearPriority = true
			} else {
				if input.Body.Priority == nil {
					var parsed int
					if err := json.Unmarshal(rawPriority, &parsed); err == nil {
						opts.SetPriority = &parsed
					}
				} else {
					opts.SetPriority = input.Body.Priority
				}
			}
		}
		if _, ok := bodyMap["work_outcomes"]; ok {
			opts.WorkOutcomesSet = true
			if input.Body.WorkOutcomes == nil {
				opts.ClearWorkOutcomes = true
			} else {
				data, err := json.Marshal(input.Body.WorkOutcomes)
				if err != nil {
					return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_outcomes", map[string]any{"error": err.Error()})
				}
				asStr := string(data)
				opts.SetWorkOutcomes = &asStr
			}
		}
		opts.AddDeps = input.Body.AddDependsOn
		opts.RemoveDeps = input.Body.RemoveDependsOn
		if isNullRaw(bodyMap["add_depends_on"]) {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "add_depends_on must be array", map[string]any{"field": "add_depends_on", "reason": "must be array"})
		}
		if isNullRaw(bodyMap["remove_depends_on"]) {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "remove_depends_on must be array", map[string]any{"field": "remove_depends_on", "reason": "must be array"})
		}
		if rawValidation, ok := bodyMap["validation"]; ok {
			opts.PolicyOverride = true
			if isNullRaw(rawValidation) {
				opts.RequiredKindsSet = true
				opts.RequiredKinds = nil
			} else {
				var validationMap map[string]json.RawMessage
				_ = json.Unmarshal(rawValidation, &validationMap)
				if rawReq, present := validationMap["require"]; present && isNullRaw(rawReq) {
					return nil, newAPIError(http.StatusBadRequest, "bad_request", "validation.require must be array", map[string]any{"field": "validation.require", "reason": "must be array"})
				}
				validation := input.Body.Validation
				if validation == nil {
					var parsed UpdateTaskValidationRequest
					if err := json.Unmarshal(rawValidation, &parsed); err == nil {
						validation = &parsed
					}
				}
				if _, present := validationMap["require"]; present {
					opts.RequiredKindsSet = true
					if validation != nil {
						opts.RequiredKinds = validation.Require
					} else {
						var parsedReq []string
						if err := json.Unmarshal(validationMap["require"], &parsedReq); err == nil {
							opts.RequiredKinds = parsedReq
						}
					}
				}
			}
		} else if input.Body.Validation != nil {
			opts.PolicyOverride = true
			opts.RequiredKindsSet = true
			opts.RequiredKinds = input.Body.Validation.Require
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

	registerWorkOutcomesUpdates(api, e)

	huma.Register(api, huma.Operation{
		OperationID: "complete-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/done",
		Summary:     "Complete task",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
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
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		if input.Body.WorkOutcomes == nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "work_outcomes is required", nil)
		}
		data, err := json.Marshal(input.Body.WorkOutcomes)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid work_outcomes", map[string]any{"error": err.Error()})
		}
		workOutcomes := string(data)
		t, err := e.TaskDone(ctx, input.ID, workOutcomes, actorID, input.Force)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID    string `path:"project_id"`
		ID           string `path:"id"`
		LeaseSeconds int    `query:"lease_seconds" default:"900"`
	}) (*struct {
		Body LeaseResponse `json:"body"`
	}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		lease, err := e.ClaimLease(ctx, input.ID, actorID, input.LeaseSeconds)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body LeaseResponse `json:"body"`
		}{Body: leaseResponse(lease)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "release-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/release",
		Summary:     "Release task lease",
		Errors: []int{
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct{}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "not_found", "task not found in project", nil)
		}
		if err := e.ReleaseLease(ctx, input.ID, actorID); err != nil {
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
		Children []treeNode   `json:"children"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "task-tree",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks/tree",
		Summary:     "Task tree",
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, input *treeInput) (*struct {
		Body []treeNode `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "task.tree"); err != nil {
			return nil, handleError(err)
		}
		tasks, err := e.Repo.ListTasks(ctx, repo.TaskFilters{ProjectID: projectID, Iteration: input.Iteration, Status: input.Status})
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
			kid := []treeNode{}
			for _, c := range children[t.ID] {
				kid = append(kid, build(c))
			}
			return treeNode{Task: taskResponse(t), Children: kid}
		}
		res := []treeNode{}
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusNotFound,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}) (*struct {
		Body ValidationStatusResponse `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "task.validation.read"); err != nil {
			return nil, handleError(err)
		}
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

func registerWorkOutcomesUpdates(api huma.API, e engine.Engine) {
	registerWorkOutcomesAppend(api, e)
	registerWorkOutcomesPut(api, e)
	registerWorkOutcomesMerge(api, e)
}

func registerWorkOutcomesAppend(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "append-task-work-outcomes",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/work-outcomes/append",
		Summary:     "Append work outcomes entry",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                    `path:"project_id"`
		ID        string                    `path:"id"`
		Body      WorkOutcomesAppendRequest `json:"body"`
	}) (*struct {
		Body WorkOutcomesUpdateResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		path := strings.TrimSpace(input.Body.Path)
		if path == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "path is required", map[string]any{"field": "path"})
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		task, length, err := mutateWorkOutcomes(ctx, e, projectID, input.ID, actorID, func(workOutcomes map[string]any) (*int, error) {
			existing, ok := workOutcomes[path]
			if !ok || existing == nil {
				workOutcomes[path] = []any{input.Body.Value}
				l := 1
				return &l, nil
			}
			list, ok := existing.([]any)
			if !ok {
				return nil, fmt.Errorf("invalid work_outcomes.%s: must be array", path)
			}
			list = append(list, input.Body.Value)
			workOutcomes[path] = list
			l := len(list)
			return &l, nil
		})
		if err != nil {
			return nil, handleError(err)
		}
		resp := WorkOutcomesUpdateResponse{
			Path:         path,
			WorkOutcomes: taskResponse(task).WorkOutcomes,
			Length:       length,
		}
		return &struct {
			Body WorkOutcomesUpdateResponse `json:"body"`
		}{Body: resp}, nil
	})
}

func registerWorkOutcomesPut(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "put-task-work-outcomes",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/work-outcomes/put",
		Summary:     "Set a work outcomes value",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                 `path:"project_id"`
		ID        string                 `path:"id"`
		Body      WorkOutcomesPutRequest `json:"body"`
	}) (*struct {
		Body WorkOutcomesUpdateResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		path := strings.TrimSpace(input.Body.Path)
		if path == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "path is required", map[string]any{"field": "path"})
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		task, _, err := mutateWorkOutcomes(ctx, e, projectID, input.ID, actorID, func(workOutcomes map[string]any) (*int, error) {
			workOutcomes[path] = input.Body.Value
			return nil, nil
		})
		if err != nil {
			return nil, handleError(err)
		}
		resp := WorkOutcomesUpdateResponse{
			Path:         path,
			WorkOutcomes: taskResponse(task).WorkOutcomes,
		}
		return &struct {
			Body WorkOutcomesUpdateResponse `json:"body"`
		}{Body: resp}, nil
	})
}

func registerWorkOutcomesMerge(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "merge-task-work-outcomes",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/work-outcomes/merge",
		Summary:     "Merge a work outcomes object",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                   `path:"project_id"`
		ID        string                   `path:"id"`
		Body      WorkOutcomesMergeRequest `json:"body"`
	}) (*struct {
		Body WorkOutcomesUpdateResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		path := strings.TrimSpace(input.Body.Path)
		if path == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "path is required", map[string]any{"field": "path"})
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		task, _, err := mutateWorkOutcomes(ctx, e, projectID, input.ID, actorID, func(workOutcomes map[string]any) (*int, error) {
			if input.Body.Value == nil {
				return nil, fmt.Errorf("invalid work_outcomes.%s: value must be object", path)
			}
			existing, ok := workOutcomes[path]
			if !ok || existing == nil {
				workOutcomes[path] = input.Body.Value
				return nil, nil
			}
			obj, ok := existing.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid work_outcomes.%s: must be object", path)
			}
			for k, v := range input.Body.Value {
				obj[k] = v
			}
			workOutcomes[path] = obj
			return nil, nil
		})
		if err != nil {
			return nil, handleError(err)
		}
		resp := WorkOutcomesUpdateResponse{
			Path:         path,
			WorkOutcomes: taskResponse(task).WorkOutcomes,
		}
		return &struct {
			Body WorkOutcomesUpdateResponse `json:"body"`
		}{Body: resp}, nil
	})
}

func registerIterations(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-iteration",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/iterations",
		Summary:       "Create iteration",
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                 `path:"project_id"`
		Body      CreateIterationRequest `json:"body"`
	}) (*struct {
		Body IterationResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
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
		res, err := e.CreateIteration(ctx, it, actorID)
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
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
		Limit     int    `query:"limit" default:"50"`
		Cursor    string `query:"cursor"`
	}) (*struct {
		Body paginatedIterations `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "iteration.list"); err != nil {
			return nil, handleError(err)
		}
		limit := normalizeLimit(input.Limit)
		cursorCreated, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		items, err := e.Repo.ListIterationsWithCursor(ctx, projectID, limit+1, cursorCreated, cursorID)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusUnprocessableEntity,
			http.StatusConflict,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
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
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		if input.Body.Status == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "status is required", nil)
		}
		it, err := e.SetIterationStatus(ctx, input.ID, input.Body.Status, actorID, input.Force)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                `path:"project_id"`
		Body      CreateDecisionRequest `json:"body"`
	}) (*struct {
		Body DecisionResponse `json:"body"`
	}, error) {
		bodyMap := rawBodyMap(ctx)
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		if input.Body.ID == "" || input.Body.Title == "" || input.Body.Decision == "" || input.Body.DeciderID == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "id, title, decision, and decider_id are required", nil)
		}
		if isNullRaw(bodyMap["rationale"]) {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "rationale must be array", map[string]any{"field": "rationale", "reason": "must be array"})
		}
		if isNullRaw(bodyMap["alternatives"]) {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "alternatives must be array", map[string]any{"field": "alternatives", "reason": "must be array"})
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
		res, err := e.CreateDecision(ctx, d, actorID)
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
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                   `path:"project_id"`
		Body      CreateAttestationRequest `json:"body"`
	}) (*struct {
		Body AttestationResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
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
		res, err := e.AddAttestation(ctx, att, actorID)
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
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, input *struct {
		ProjectID  string `path:"project_id"`
		EntityKind string `query:"entity_kind" enum:"project,iteration,task,decision"`
		EntityID   string `query:"entity_id"`
		Kind       string `query:"kind"`
		Limit      int    `query:"limit" default:"50"`
		Cursor     string `query:"cursor"`
	}) (*struct {
		Body paginatedAttestations `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "attestation.list"); err != nil {
			return nil, handleError(err)
		}
		limit := normalizeLimit(input.Limit)
		cursorTS, cursorID, err := parseCompositeCursor(input.Cursor)
		if err != nil {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
		}
		f := repo.AttestationFilters{
			ProjectID:  projectID,
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
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, input *struct {
		ProjectID  string `path:"project_id"`
		Type       string `query:"type"`
		EntityKind string `query:"entity_kind" enum:"project,iteration,task,decision,rbac"`
		EntityID   string `query:"entity_id"`
		Limit      int    `query:"limit" default:"50"`
		Cursor     string `query:"cursor"`
	}) (*struct {
		Body paginatedEvents `json:"body"`
	}, error) {
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := requirePermission(ctx, e, projectID, "project.events.read"); err != nil {
			return nil, handleError(err)
		}
		limit := normalizeLimit(input.Limit)
		var cursorID int64
		if input.Cursor != "" {
			parsed, err := strconv.ParseInt(input.Cursor, 10, 64)
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "bad_request", "invalid cursor", map[string]any{"cursor": input.Cursor})
			}
			cursorID = parsed
		}
		items, err := e.Repo.LatestEventsFrom(ctx, limit+1, cursorID, projectID, input.Type, input.EntityKind, input.EntityID)
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

func registerRBAC(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "whoami",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/me/permissions",
		Summary:     "Current actor permissions",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusNotFound,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body WhoAmIResponse `json:"body"`
	}, error) {
		principal, authErr := principalFromRequest(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		who, err := e.WhoAmI(ctx, projectID, principal.ActorID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body WhoAmIResponse `json:"body"`
		}{Body: WhoAmIResponse{
			ActorID:     who.ActorID,
			OrgID:       principal.OrgID,
			Roles:       nonNilSlice(who.Roles),
			Permissions: nonNilSlice(who.Permissions),
		}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "grant-role",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/rbac/roles/grant",
		Summary:     "Grant role",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string            `path:"project_id"`
		Body      RoleChangeRequest `json:"body"`
	}) (*struct{}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := e.GrantRole(ctx, projectID, actorID, input.Body.ActorID, input.Body.RoleID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revoke-role",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/rbac/roles/revoke",
		Summary:     "Revoke role",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string            `path:"project_id"`
		Body      RoleChangeRequest `json:"body"`
	}) (*struct{}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := e.RevokeRole(ctx, projectID, actorID, input.Body.ActorID, input.Body.RoleID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "allow-attestation-role",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/rbac/attestations/allow",
		Summary:     "Allow attestation role",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                      `path:"project_id"`
		Body      AttestationAuthorityRequest `json:"body"`
	}) (*struct{}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := e.AllowAttestationRole(ctx, projectID, actorID, input.Body.Kind, input.Body.RoleID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deny-attestation-role",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/rbac/attestations/deny",
		Summary:     "Deny attestation role",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusForbidden,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		ProjectID string                      `path:"project_id"`
		Body      AttestationAuthorityRequest `json:"body"`
	}) (*struct{}, error) {
		actorID, authErr := actorIDFromContext(ctx)
		if authErr != nil {
			return nil, authErr
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		if err := e.DenyAttestationRole(ctx, projectID, actorID, input.Body.Kind, input.Body.RoleID); err != nil {
			return nil, handleError(err)
		}
		return &struct{}{}, nil
	})
}

func registerMe(api huma.API, e engine.Engine) {
	huma.Register(api, huma.Operation{
		OperationID: "me",
		Method:      http.MethodGet,
		Path:        "/me",
		Summary:     "Current principal",
		Errors: []int{
			http.StatusUnauthorized,
		},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body WhoAmIResponse `json:"body"`
	}, error) {
		principal, authErr := principalFromRequest(ctx)
		if authErr != nil {
			return nil, authErr
		}
		roles := principal.Roles
		perms := principal.Permissions
		if len(perms) == 0 && e.Config != nil {
			if who, err := e.WhoAmI(ctx, e.Config.Project.ID, principal.ActorID); err == nil {
				if len(roles) == 0 {
					roles = who.Roles
				}
				perms = who.Permissions
			}
		}
		return &struct {
			Body WhoAmIResponse `json:"body"`
		}{Body: WhoAmIResponse{
			ActorID:     principal.ActorID,
			OrgID:       principal.OrgID,
			Roles:       nonNilSlice(roles),
			Permissions: nonNilSlice(perms),
		}}, nil
	})
}

func registerDevAuth(api huma.API, e engine.Engine, authCfg AuthConfig) {
	huma.Register(api, huma.Operation{
		OperationID: "dev-login",
		Method:      http.MethodPost,
		Path:        "/auth/dev/login",
		Summary:     "DEV ONLY: mint a JWT for local testing",
		Errors: []int{
			http.StatusBadRequest,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, input *struct {
		Body DevLoginRequest `json:"body"`
	}) (*struct {
		Body DevLoginResponse `json:"body"`
	}, error) {
		if len(bodyBytes(ctx)) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "body required", nil)
		}
		actor := strings.TrimSpace(input.Body.ActorID)
		org := strings.TrimSpace(input.Body.OrgID)
		if actor == "" || org == "" {
			return nil, newAPIError(http.StatusBadRequest, "bad_request", "actor_id and org_id are required", nil)
		}
		token, err := signDevToken(authCfg.JWTSecret, actor, org, input.Body.Roles, input.Body.Scopes)
		if err != nil {
			return nil, newAPIError(http.StatusInternalServerError, "internal_error", err.Error(), nil)
		}
		return &struct {
			Body DevLoginResponse `json:"body"`
		}{Body: DevLoginResponse{Token: token}}, nil
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

func isNullRaw(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && bytes.Equal(trimmed, []byte("null"))
}

func parseWorkOutcomesMap(raw *string) (map[string]any, error) {
	if raw == nil || *raw == "" {
		return map[string]any{}, nil
	}
	var tmp any
	if err := json.Unmarshal([]byte(*raw), &tmp); err != nil {
		return nil, fmt.Errorf("invalid work_outcomes: %w", err)
	}
	obj, ok := tmp.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid work_outcomes: must be object")
	}
	return obj, nil
}

func mutateWorkOutcomes(
	ctx context.Context,
	e engine.Engine,
	projectID string,
	taskID string,
	actorID string,
	mutate func(map[string]any) (*int, error),
) (domain.Task, *int, error) {
	if err := requirePermission(ctx, e, projectID, "task.update"); err != nil {
		return domain.Task{}, nil, err
	}
	task, err := e.Repo.GetTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, nil, err
	}
	if !projectMatches(projectID, task.ProjectID) {
		return domain.Task{}, nil, repo.ErrNotFound
	}
	if _, err := e.ClaimLease(ctx, taskID, actorID, 60); err != nil {
		return domain.Task{}, nil, err
	}
	defer func() {
		_ = e.ReleaseLease(ctx, taskID, actorID)
	}()
	task, err = e.Repo.GetTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, nil, err
	}
	workOutcomes, err := parseWorkOutcomesMap(task.WorkOutcomesJSON)
	if err != nil {
		return domain.Task{}, nil, err
	}
	length, err := mutate(workOutcomes)
	if err != nil {
		return domain.Task{}, nil, err
	}
	data, err := json.Marshal(workOutcomes)
	if err != nil {
		return domain.Task{}, nil, fmt.Errorf("invalid work_outcomes: %w", err)
	}
	encoded := string(data)
	opts := engine.TaskUpdateOptions{
		ID:              taskID,
		ActorID:         actorID,
		WorkOutcomesSet: true,
		SetWorkOutcomes: &encoded,
	}
	updated, err := e.UpdateTask(ctx, opts)
	if err != nil {
		return domain.Task{}, nil, err
	}
	return updated, length, nil
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
	required := decodeStringSlice(t.RequiredAttestationsJSON)
	resp := ValidationStatusResponse{
		Required: nonNilSlice(required),
		Present:  []string{},
		Missing:  []string{},
	}
	if len(required) == 0 {
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
	resp.Satisfied = len(resp.Missing) == 0
	return resp, nil
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
