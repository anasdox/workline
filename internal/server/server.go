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
	"reflect"
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
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
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
		return newAPIError(status, msg, nil)
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

func newAPIError(status int, message string, details any) huma.StatusError {
	code := strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
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
		return newAPIError(http.StatusNotFound, err.Error(), nil)
	}
	msg := err.Error()
	lowered := strings.ToLower(msg)
	switch {
	case strings.Contains(lowered, "lease") && (strings.Contains(lowered, "held") || strings.Contains(lowered, "owned")):
		return newAPIError(http.StatusConflict, msg, nil)
	case strings.Contains(lowered, "lease required"):
		return newAPIError(http.StatusConflict, msg, nil)
	case strings.Contains(lowered, "not done"),
		strings.Contains(lowered, "validation"),
		strings.Contains(lowered, "required for iteration validation"):
		return newAPIError(http.StatusUnprocessableEntity, msg, nil)
	case strings.Contains(lowered, "invalid") || strings.Contains(lowered, "missing") || strings.Contains(lowered, "required"):
		return newAPIError(http.StatusBadRequest, msg, nil)
	default:
		return newAPIError(http.StatusInternalServerError, "internal error", msg)
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
		OperationID: "create-project",
		Method:      http.MethodPost,
		Path:        "/projects",
		Summary:     "Create project",
	}, func(ctx context.Context, input *struct {
		ActorID string `header:"X-Actor-Id" default:"local-user"`
		Body    struct {
			ID          string `json:"id"`
			Description string `json:"description,omitempty"`
		} `json:"body"`
	}) (*struct {
		Body domain.Project `json:"body"`
	}, error) {
		if input.Body.ID == "" {
			return nil, newAPIError(http.StatusBadRequest, "id is required", nil)
		}
		p, err := e.InitProject(ctx, input.Body.ID, input.Body.Description, input.ActorID)
		if err != nil {
			return nil, handleError(err)
		}
		if err := e.Repo.UpsertProjectConfig(ctx, p.ID, config.Default(p.ID)); err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Project `json:"body"`
		}{Body: p}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
		Summary:     "List projects",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []domain.Project `json:"body"`
	}, error) {
		items, err := e.Repo.ListProjects(ctx)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []domain.Project `json:"body"`
		}{Body: items}, nil
	})
}

func registerTasks(api huma.API, e engine.Engine) {
	type taskCreateBody struct {
		ID                string   `json:"id,omitempty"`
		IterationID       string   `json:"iteration_id,omitempty"`
		ParentID          string   `json:"parent_id,omitempty"`
		Type              string   `json:"type,omitempty"`
		Title             string   `json:"title"`
		Description       string   `json:"description,omitempty"`
		DependsOn         []string `json:"depends_on,omitempty"`
		AssigneeID        string   `json:"assignee_id,omitempty"`
		PolicyPreset      string   `json:"policy_preset,omitempty"`
		ValidationMode    string   `json:"validation_mode,omitempty"`
		RequiredKinds     []string `json:"required_kinds,omitempty"`
		RequiredThreshold *int     `json:"required_threshold,omitempty"`
	}
	type taskCreateInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		Body      json.RawMessage `json:"body"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "create-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks",
		Summary:     "Create task",
	}, func(ctx context.Context, input *taskCreateInput) (*struct {
		Body domain.Task `json:"body"`
	}, error) {
		var body taskCreateBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		opts := engine.TaskCreateOptions{
			ID:                body.ID,
			ProjectID:         projectID,
			IterationID:       body.IterationID,
			ParentID:          body.ParentID,
			Type:              body.Type,
			Title:             body.Title,
			Description:       body.Description,
			AssigneeID:        body.AssigneeID,
			DependsOn:         body.DependsOn,
			PolicyPreset:      body.PolicyPreset,
			ValidationMode:    body.ValidationMode,
			RequiredKinds:     body.RequiredKinds,
			RequiredThreshold: optionalInt(body.RequiredThreshold),
			ActorID:           input.ActorID,
		}
		if opts.Type == "" {
			opts.Type = "technical"
		}
		if body.ValidationMode != "" || len(body.RequiredKinds) > 0 || body.RequiredThreshold != nil {
			opts.PolicyOverride = true
		}
		t, err := e.CreateTask(ctx, opts)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Task `json:"body"`
		}{Body: t}, nil
	})

	type listTasksInput struct {
		ProjectID   string `path:"project_id"`
		Status      string `query:"status"`
		IterationID string `query:"iteration_id"`
		ParentID    string `query:"parent_id"`
		AssigneeID  string `query:"assignee_id"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "list-tasks",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks",
		Summary:     "List tasks",
	}, func(ctx context.Context, input *listTasksInput) (*struct {
		Body []domain.Task `json:"body"`
	}, error) {
		filter := repo.TaskFilters{
			ProjectID:  projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID),
			Status:     input.Status,
			Iteration:  input.IterationID,
			Parent:     input.ParentID,
			AssigneeID: input.AssigneeID,
		}
		tasks, err := e.Repo.ListTasks(ctx, filter)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []domain.Task `json:"body"`
		}{Body: tasks}, nil
	})

	type taskIDPath struct {
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "get-task",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/tasks/{id}",
		Summary:     "Get task",
	}, func(ctx context.Context, input *taskIDPath) (*struct {
		Body domain.Task `json:"body"`
	}, error) {
		t, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "task not found in project", nil)
		}
		return &struct {
			Body domain.Task `json:"body"`
		}{Body: t}, nil
	})

	type taskUpdateBody struct {
		Status           string           `json:"status,omitempty"`
		Assign           *string          `json:"assign,omitempty"`
		AddDependsOn     []string         `json:"add_depends_on,omitempty"`
		RemoveDependsOn  []string         `json:"remove_depends_on,omitempty"`
		SetParent        *string          `json:"set_parent,omitempty"`
		SetWorkProofJSON *json.RawMessage `json:"set_work_proof_json,omitempty"`
		SetPolicy        string           `json:"set_policy,omitempty"`
		ValidationMode   string           `json:"validation_mode,omitempty"`
		RequiredKinds    []string         `json:"required_kinds,omitempty"`
		Threshold        *int             `json:"threshold,omitempty"`
	}
	type taskUpdateInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		ID        string          `path:"id"`
		Body      json.RawMessage `json:"body"`
		Force     bool            `query:"force"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "update-task",
		Method:      http.MethodPatch,
		Path:        "/projects/{project_id}/tasks/{id}",
		Summary:     "Update task",
	}, func(ctx context.Context, input *taskUpdateInput) (*struct {
		Body domain.Task `json:"body"`
	}, error) {
		var body taskUpdateBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		opts := engine.TaskUpdateOptions{
			ID:             input.ID,
			Status:         body.Status,
			Assign:         body.Assign,
			AddDeps:        body.AddDependsOn,
			RemoveDeps:     body.RemoveDependsOn,
			SetParent:      body.SetParent,
			PolicyPreset:   body.SetPolicy,
			ValidationMode: body.ValidationMode,
			RequiredKinds:  body.RequiredKinds,
			Threshold:      body.Threshold,
			ActorID:        input.ActorID,
			Force:          input.Force,
		}
		if body.SetWorkProofJSON != nil {
			serialized, err := body.SetWorkProofJSON.MarshalJSON()
			if err != nil {
				return nil, newAPIError(http.StatusBadRequest, "invalid work proof json", err.Error())
			}
			jsonStr := string(serialized)
			opts.SetWorkProof = &jsonStr
		}
		if body.ValidationMode != "" || len(body.RequiredKinds) > 0 || body.Threshold != nil {
			opts.PolicyOverride = true
		}
		t, err := e.UpdateTask(ctx, opts)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "task not found in project", nil)
		}
		return &struct {
			Body domain.Task `json:"body"`
		}{Body: t}, nil
	})

	type taskDoneBody struct {
		WorkProof json.RawMessage `json:"work_proof"`
	}
	type taskDoneInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		ID        string          `path:"id"`
		Body      json.RawMessage `json:"body"`
		Force     bool            `query:"force"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "complete-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/done",
		Summary:     "Complete task",
	}, func(ctx context.Context, input *taskDoneInput) (*struct {
		Body domain.Task `json:"body"`
	}, error) {
		var body taskDoneBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		if len(body.WorkProof) == 0 {
			return nil, newAPIError(http.StatusBadRequest, "work_proof is required", nil)
		}
		workProof := string(body.WorkProof)
		t, err := e.TaskDone(ctx, input.ID, workProof, input.ActorID, input.Force)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, t.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "task not found in project", nil)
		}
		return &struct {
			Body domain.Task `json:"body"`
		}{Body: t}, nil
	})

	type claimInput struct {
		ActorID      string `header:"X-Actor-Id" default:"local-user"`
		ProjectID    string `path:"project_id"`
		ID           string `path:"id"`
		LeaseSeconds int    `query:"lease_seconds" default:"900"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "claim-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/claim",
		Summary:     "Claim task lease",
	}, func(ctx context.Context, input *claimInput) (*struct {
		Body domain.Lease `json:"body"`
	}, error) {
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "task not found in project", nil)
		}
		lease, err := e.ClaimLease(ctx, input.ID, input.ActorID, input.LeaseSeconds)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Lease `json:"body"`
		}{Body: lease}, nil
	})

	type releaseInput struct {
		ActorID   string `header:"X-Actor-Id" default:"local-user"`
		ProjectID string `path:"project_id"`
		ID        string `path:"id"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "release-task",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/tasks/{id}/release",
		Summary:     "Release task lease",
	}, func(ctx context.Context, input *releaseInput) (*struct{}, error) {
		task, err := e.Repo.GetTask(ctx, input.ID)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, task.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "task not found in project", nil)
		}
		if err := e.ReleaseLease(ctx, input.ID, input.ActorID); err != nil {
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
		Task     domain.Task `json:"task"`
		Children []treeNode  `json:"children,omitempty"`
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
			return treeNode{Task: t, Children: kid}
		}
		var res []treeNode
		for _, r := range roots {
			res = append(res, build(r))
		}
		return &struct {
			Body []treeNode `json:"body"`
		}{Body: res}, nil
	})
}

func registerIterations(api huma.API, e engine.Engine) {
	type iterationCreateBody struct {
		ID   string `json:"id"`
		Goal string `json:"goal"`
	}
	type iterationCreateInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		Body      json.RawMessage `json:"body"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "create-iteration",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/iterations",
		Summary:     "Create iteration",
	}, func(ctx context.Context, input *iterationCreateInput) (*struct {
		Body domain.Iteration `json:"body"`
	}, error) {
		var body iterationCreateBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		bodyProject := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		it := domain.Iteration{
			ID:        body.ID,
			ProjectID: bodyProject,
			Goal:      body.Goal,
		}
		res, err := e.CreateIteration(ctx, it, input.ActorID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Iteration `json:"body"`
		}{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-iterations",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/iterations",
		Summary:     "List iterations",
	}, func(ctx context.Context, input *struct {
		ProjectID string `path:"project_id"`
	}) (*struct {
		Body []domain.Iteration `json:"body"`
	}, error) {
		items, err := e.Repo.ListIterations(ctx, projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID))
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []domain.Iteration `json:"body"`
		}{Body: items}, nil
	})

	type iterationStatusBody struct {
		Status string `json:"status"`
	}
	type iterationStatusInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		ID        string          `path:"id"`
		Body      json.RawMessage `json:"body"`
		Force     bool            `query:"force"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "set-iteration-status",
		Method:      http.MethodPatch,
		Path:        "/projects/{project_id}/iterations/{id}/status",
		Summary:     "Update iteration status",
	}, func(ctx context.Context, input *iterationStatusInput) (*struct {
		Body domain.Iteration `json:"body"`
	}, error) {
		var body iterationStatusBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		it, err := e.SetIterationStatus(ctx, input.ID, body.Status, input.ActorID, input.Force)
		if err != nil {
			return nil, handleError(err)
		}
		if !projectMatches(input.ProjectID, it.ProjectID) {
			return nil, newAPIError(http.StatusNotFound, "iteration not found in project", nil)
		}
		return &struct {
			Body domain.Iteration `json:"body"`
		}{Body: it}, nil
	})
}

func registerDecisions(api huma.API, e engine.Engine) {
	type decisionCreateBody struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Decision     string   `json:"decision"`
		Rationale    []string `json:"rationale,omitempty"`
		Alternatives []string `json:"alternatives,omitempty"`
		ContextJSON  string   `json:"context_json,omitempty"`
		DeciderID    string   `json:"decider_id"`
	}
	type decisionCreateInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		Body      json.RawMessage `json:"body"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "create-decision",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/decisions",
		Summary:     "Create decision",
	}, func(ctx context.Context, input *decisionCreateInput) (*struct {
		Body domain.Decision `json:"body"`
	}, error) {
		var body decisionCreateBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		d := domain.Decision{
			ID:               body.ID,
			ProjectID:        projectID,
			Title:            body.Title,
			Decision:         body.Decision,
			ContextJSON:      body.ContextJSON,
			RationaleJSON:    toJSONArray(body.Rationale),
			AlternativesJSON: toJSONArray(body.Alternatives),
			DeciderID:        body.DeciderID,
		}
		res, err := e.CreateDecision(ctx, d, input.ActorID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Decision `json:"body"`
		}{Body: res}, nil
	})
}

func registerAttestations(api huma.API, e engine.Engine) {
	type attestationBody struct {
		EntityKind string `json:"entity_kind"`
		EntityID   string `json:"entity_id"`
		Kind       string `json:"kind"`
		Payload    any    `json:"payload,omitempty"`
	}
	type attestationInput struct {
		ActorID   string          `header:"X-Actor-Id" default:"local-user"`
		ProjectID string          `path:"project_id"`
		Body      json.RawMessage `json:"body"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "add-attestation",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/attestations",
		Summary:     "Add attestation",
	}, func(ctx context.Context, input *attestationInput) (*struct {
		Body domain.Attestation `json:"body"`
	}, error) {
		var body attestationBody
		if err := decodeBody(input.Body, ctx, &body); err != nil {
			return nil, err
		}
		projectID := projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID)
		payload := ""
		if body.Payload != nil {
			b, _ := json.Marshal(body.Payload)
			payload = string(b)
		}
		att := domain.Attestation{
			ProjectID:   projectID,
			EntityKind:  body.EntityKind,
			EntityID:    body.EntityID,
			Kind:        body.Kind,
			PayloadJSON: payload,
		}
		res, err := e.AddAttestation(ctx, att, input.ActorID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body domain.Attestation `json:"body"`
		}{Body: res}, nil
	})

	type listAttestInput struct {
		ProjectID  string `path:"project_id"`
		EntityKind string `query:"entity_kind"`
		EntityID   string `query:"entity_id"`
		Kind       string `query:"kind"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "list-attestations",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/attestations",
		Summary:     "List attestations",
	}, func(ctx context.Context, input *listAttestInput) (*struct {
		Body []domain.Attestation `json:"body"`
	}, error) {
		f := repo.AttestationFilters{
			ProjectID:  projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID),
			EntityKind: input.EntityKind,
			EntityID:   input.EntityID,
			Kind:       input.Kind,
		}
		items, err := e.Repo.ListAttestations(ctx, f)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []domain.Attestation `json:"body"`
		}{Body: items}, nil
	})
}

func registerEvents(api huma.API, e engine.Engine) {
	type eventsInput struct {
		ProjectID  string `path:"project_id"`
		Type       string `query:"type"`
		EntityKind string `query:"entity_kind"`
		EntityID   string `query:"entity_id"`
		Limit      int    `query:"limit" default:"20"`
	}
	huma.Register(api, huma.Operation{
		OperationID: "list-events",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/events",
		Summary:     "List recent events",
	}, func(ctx context.Context, input *eventsInput) (*struct {
		Body []domain.Event `json:"body"`
	}, error) {
		if input.Limit <= 0 {
			input.Limit = 20
		}
		items, err := e.Repo.LatestEvents(ctx, input.Limit, projectFromPathOrHeader(ctx, input.ProjectID, e.Config.Project.ID), input.Type, input.EntityKind, input.EntityID)
		if err != nil {
			return nil, handleError(err)
		}
		return &struct {
			Body []domain.Event `json:"body"`
		}{Body: items}, nil
	})
}

func decodeBody(raw json.RawMessage, ctx context.Context, dest any) huma.StatusError {
	data := raw
	if len(data) == 0 {
		if buf, ok := ctx.Value(bodyBytesKey{}).([]byte); ok {
			data = buf
		} else {
			req, ok := ctx.Value(requestKey{}).(*http.Request)
			if !ok {
				return newAPIError(http.StatusInternalServerError, "request unavailable", nil)
			}
			buf, err := io.ReadAll(req.Body)
			if err != nil {
				return newAPIError(http.StatusBadRequest, "invalid body", err.Error())
			}
			data = buf
		}
	}
	if len(data) == 0 {
		return newAPIError(http.StatusBadRequest, "body required", nil)
	}
	if err := json.Unmarshal(data, dest); err == nil {
		if !isZero(dest) {
			return nil
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return newAPIError(http.StatusBadRequest, "invalid body", err.Error())
	}
	if inner, ok := envelope["body"]; ok {
		if err := json.Unmarshal(inner, dest); err != nil {
			return newAPIError(http.StatusBadRequest, "invalid body", err.Error())
		}
		if isZero(dest) {
			return newAPIError(http.StatusBadRequest, "body empty", nil)
		}
		return nil
	}
	return newAPIError(http.StatusBadRequest, "invalid body", nil)
}

func isZero(v any) bool {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	return val.IsZero()
}

func optionalInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func toJSONArray(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
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
