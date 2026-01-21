package server

import (
	"encoding/json"

	"workline/internal/config"
	"workline/internal/domain"
)

// Request payloads

type CreateProjectRequest struct {
	ID          string  `json:"id"`
	OrgID       string  `json:"org_id"`
	Description *string `json:"description,omitempty"`
}

type TaskValidationRequest struct {
	Require []string `json:"require,omitempty" example:"[\"ci.passed\",\"review.approved\"]"`
}

type TaskPolicyRequest struct {
	Preset string `json:"preset,omitempty" example:"done"`
}

type CreateTaskRequest struct {
	ID           *string                `json:"id,omitempty" example:"task-auth-1"`
	IterationID  *string                `json:"iteration_id,omitempty" example:"iter-1"`
	ParentID     *string                `json:"parent_id,omitempty" example:"task-epic"`
	Type         string                 `json:"type" example:"feature"`
	Title        string                 `json:"title" example:"Ship authentication"`
	Description  *string                `json:"description,omitempty" example:"Implement login and SSO flows"`
	AssigneeID   *string                `json:"assignee_id,omitempty" example:"dev-1"`
	Priority     *int                   `json:"priority,omitempty" example:"1"`
	DependsOn    []string               `json:"depends_on,omitempty" example:"[\"task-seed\"]"`
	Policy       *TaskPolicyRequest     `json:"policy,omitempty"`
	Validation   *TaskValidationRequest `json:"validation,omitempty"`
	WorkOutcomes map[string]any         `json:"work_outcomes,omitempty" example:"{\"pr\":123}"`
}

type SubtaskRequest struct {
	ID           *string                `json:"id,omitempty" example:"task-auth-1-1"`
	LocalID      *string                `json:"local_id,omitempty" example:"auth-ui"`
	IterationID  *string                `json:"iteration_id,omitempty" example:"iter-1"`
	Type         string                 `json:"type,omitempty" example:"feature"`
	Title        string                 `json:"title" example:"Ship authentication UI"`
	Description  *string                `json:"description,omitempty" example:"Build login screens and flows"`
	AssigneeID   *string                `json:"assignee_id,omitempty" example:"dev-1"`
	Priority     *int                   `json:"priority,omitempty" example:"1"`
	DependsOn    []string               `json:"depends_on,omitempty" example:"[\"auth-api\"]"`
	Policy       *TaskPolicyRequest     `json:"policy,omitempty"`
	Validation   *TaskValidationRequest `json:"validation,omitempty"`
	WorkOutcomes map[string]any         `json:"work_outcomes,omitempty" example:"{\"spec\":\"done\"}"`
}

type DecomposeTaskRequest struct {
	Subtasks []SubtaskRequest `json:"subtasks"`
}

type DecomposeTaskResponse struct {
	Parent   TaskResponse      `json:"parent"`
	Subtasks []TaskResponse    `json:"subtasks"`
	Mapping  map[string]string `json:"mapping,omitempty"`
}

type ComposeTaskRequest struct {
	Result       string         `json:"result" example:"Summary of task outcomes"`
	Summary      *string        `json:"summary,omitempty" example:"Short summary"`
	WorkOutcomes map[string]any `json:"work_outcomes,omitempty" example:"{\"prd\":\"...\"}"`
}

type UpdateTaskValidationRequest struct {
	Require []string `json:"require,omitempty"`
}

type UpdateTaskRequest struct {
	Status          *string                      `json:"status,omitempty" enum:"planned,in_progress,review,done,rejected,canceled"`
	AssigneeID      *string                      `json:"assignee_id,omitempty"`
	AddDependsOn    []string                     `json:"add_depends_on,omitempty"`
	RemoveDependsOn []string                     `json:"remove_depends_on,omitempty"`
	ParentID        *string                      `json:"parent_id,omitempty"`
	Priority        *int                         `json:"priority,omitempty"`
	WorkOutcomes    *map[string]any              `json:"work_outcomes,omitempty"`
	Validation      *UpdateTaskValidationRequest `json:"validation,omitempty"`
}

type CompleteTaskRequest struct {
	WorkOutcomes map[string]any `json:"work_outcomes"`
}

type WorkOutcomesAppendRequest struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type WorkOutcomesPutRequest struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type WorkOutcomesMergeRequest struct {
	Path  string         `json:"path"`
	Value map[string]any `json:"value"`
}

type CreateIterationRequest struct {
	ID   string `json:"id"`
	Goal string `json:"goal"`
}

type SetIterationStatusRequest struct {
	Status string `json:"status" enum:"pending,running,delivered,validated,rejected"`
}

type CreateDecisionRequest struct {
	ID           string         `json:"id" example:"dec-1"`
	Title        string         `json:"title" example:"Choose runtime"`
	Decision     string         `json:"decision" example:"Adopt Go for backend"`
	DeciderID    string         `json:"decider_id" example:"cto-1"`
	Context      map[string]any `json:"context,omitempty"`
	Rationale    []string       `json:"rationale,omitempty" example:"[\"Team experience\",\"Ecosystem support\"]"`
	Alternatives []string       `json:"alternatives,omitempty" example:"[\"Rust\",\"NodeJS\"]"`
}

type CreateAttestationRequest struct {
	ID         *string        `json:"id,omitempty" example:"att-1"`
	EntityKind string         `json:"entity_kind" enum:"project,iteration,task,decision" example:"task"`
	EntityID   string         `json:"entity_id" example:"task-auth-1"`
	Kind       string         `json:"kind" example:"review.approved"`
	TS         *string        `json:"ts,omitempty" format:"date-time" example:"2024-05-01T10:00:00Z"`
	Payload    map[string]any `json:"payload,omitempty" example:"{\"note\":\"LGTM\"}"`
}

// Response payloads

type ProjectResponse struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at" format:"date-time"`
}

type IterationResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Goal      string `json:"goal"`
	Status    string `json:"status" enum:"pending,running,delivered,validated,rejected"`
	CreatedAt string `json:"created_at" format:"date-time"`
}

type TaskResponse struct {
	ID                   string         `json:"id" example:"task-auth-1"`
	ProjectID            string         `json:"project_id" example:"workline"`
	IterationID          *string        `json:"iteration_id,omitempty" example:"iter-1"`
	ParentID             *string        `json:"parent_id,omitempty" example:"task-epic"`
	Type                 string         `json:"type" example:"feature"`
	Title                string         `json:"title" example:"Ship authentication"`
	Description          string         `json:"description,omitempty" example:"Implement login and SSO flows"`
	Status               string         `json:"status" enum:"planned,in_progress,review,done,rejected,canceled" example:"planned"`
	AssigneeID           *string        `json:"assignee_id,omitempty" example:"dev-1"`
	Priority             *int           `json:"priority,omitempty" example:"1"`
	WorkOutcomes         map[string]any `json:"work_outcomes,omitempty" example:"{\"pr\":123}"`
	RequiredAttestations []string       `json:"required_attestations" example:"[\"ci.passed\",\"review.approved\"]"`
	DependsOn            []string       `json:"depends_on" example:"[]"`
	CreatedAt            string         `json:"created_at" format:"date-time" example:"2024-05-01T09:00:00Z"`
	UpdatedAt            string         `json:"updated_at" format:"date-time" example:"2024-05-01T09:05:00Z"`
	CompletedAt          *string        `json:"completed_at" format:"date-time" example:"2024-05-02T10:00:00Z"`
}

type DecisionResponse struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	Title        string         `json:"title"`
	Decision     string         `json:"decision"`
	DeciderID    string         `json:"decider_id"`
	Context      map[string]any `json:"context,omitempty"`
	Rationale    []string       `json:"rationale"`
	Alternatives []string       `json:"alternatives"`
	CreatedAt    string         `json:"created_at" format:"date-time"`
}

type LeaseResponse struct {
	TaskID     string `json:"task_id"`
	OwnerID    string `json:"owner_id"`
	AcquiredAt string `json:"acquired_at" format:"date-time"`
	ExpiresAt  string `json:"expires_at" format:"date-time"`
}

type WorkOutcomesUpdateResponse struct {
	Path         string         `json:"path"`
	WorkOutcomes map[string]any `json:"work_outcomes"`
	Length       *int           `json:"length,omitempty"`
}

type AttestationResponse struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	EntityKind string         `json:"entity_kind" enum:"project,iteration,task,decision"`
	EntityID   string         `json:"entity_id"`
	Kind       string         `json:"kind"`
	ActorID    string         `json:"actor_id"`
	TS         string         `json:"ts" format:"date-time"`
	Payload    map[string]any `json:"payload,omitempty"`
}

type EventResponse struct {
	ID         int64          `json:"id"`
	TS         string         `json:"ts" format:"date-time"`
	Type       string         `json:"type"`
	ProjectID  string         `json:"project_id,omitempty"`
	EntityKind string         `json:"entity_kind" enum:"project,iteration,task,decision,rbac"`
	EntityID   string         `json:"entity_id,omitempty"`
	ActorID    string         `json:"actor_id"`
	Payload    map[string]any `json:"payload"`
}

type ValidationStatusResponse struct {
	Required  []string `json:"required" example:"[\"ci.passed\",\"review.approved\"]"`
	Present   []string `json:"present" example:"[\"ci.passed\"]"`
	Missing   []string `json:"missing" example:"[\"review.approved\"]"`
	Satisfied bool     `json:"satisfied" example:"false"`
}

type ProjectConfigResponse struct {
	Project projectConfigSection `json:"project"`
}

type projectConfigSection struct {
	ID             string                                 `json:"id"`
	TaskTypes      map[string]taskTypeConfigResponse      `json:"task_types"`
	IterationTypes map[string]iterationTypeConfigResponse `json:"iteration_types,omitempty"`
	Attestations   []attestationConfigResponse            `json:"attestations"`
	RBAC           rbacConfigResponse                     `json:"rbac"`
}

type taskTypeConfigResponse struct {
	Policies map[string]policyRuleResponse `json:"policies"`
}

type iterationTypeConfigResponse struct {
	Policies map[string]policyRuleResponse `json:"policies"`
}

type policyRuleResponse struct {
	All []string `json:"all"`
}

type attestationConfigResponse struct {
	ID          string `json:"id"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description"`
}

type rbacConfigResponse struct {
	Permissions map[string][]string         `json:"permissions"`
	Roles       map[string]rbacRoleResponse `json:"roles"`
}

type rbacRoleResponse struct {
	Description string   `json:"description"`
	Grants      []string `json:"grants"`
	CanAttest   []string `json:"can_attest,omitempty"`
}

type paginatedTasks struct {
	Items      []TaskResponse `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type paginatedIterations struct {
	Items      []IterationResponse `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type paginatedAttestations struct {
	Items      []AttestationResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type paginatedEvents struct {
	Items      []EventResponse `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type RoleChangeRequest struct {
	ActorID string `json:"actor_id"`
	RoleID  string `json:"role_id"`
}

type AttestationAuthorityRequest struct {
	Kind   string `json:"kind"`
	RoleID string `json:"role_id"`
}

type WhoAmIResponse struct {
	ActorID     string   `json:"actor_id"`
	OrgID       string   `json:"org_id"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

type DevLoginRequest struct {
	ActorID string   `json:"actor_id"`
	OrgID   string   `json:"org_id"`
	Roles   []string `json:"roles,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
}

type DevLoginResponse struct {
	Token string `json:"token"`
}

// Conversion helpers

func projectResponse(p domain.Project) ProjectResponse {
	return ProjectResponse{
		ID:          p.ID,
		OrgID:       p.OrgID,
		Kind:        p.Kind,
		Status:      p.Status,
		Description: p.Description,
		CreatedAt:   p.CreatedAt,
	}
}

func iterationResponse(it domain.Iteration) IterationResponse {
	return IterationResponse{
		ID:        it.ID,
		ProjectID: it.ProjectID,
		Goal:      it.Goal,
		Status:    it.Status,
		CreatedAt: it.CreatedAt,
	}
}

func taskResponse(t domain.Task) TaskResponse {
	req := decodeStringSlice(t.RequiredAttestationsJSON)
	workOutcomes := decodeJSONMap(t.WorkOutcomesJSON)
	return TaskResponse{
		ID:                   t.ID,
		ProjectID:            t.ProjectID,
		IterationID:          t.IterationID,
		ParentID:             t.ParentID,
		Type:                 t.Type,
		Title:                t.Title,
		Description:          t.Description,
		Status:               t.Status,
		AssigneeID:           t.AssigneeID,
		Priority:             t.Priority,
		WorkOutcomes:         workOutcomes,
		RequiredAttestations: nonNilSlice(req),
		DependsOn:            nonNilSlice(t.DependsOn),
		CreatedAt:            t.CreatedAt,
		UpdatedAt:            t.UpdatedAt,
		CompletedAt:          t.CompletedAt,
	}
}

func decisionResponse(d domain.Decision) DecisionResponse {
	return DecisionResponse{
		ID:           d.ID,
		ProjectID:    d.ProjectID,
		Title:        d.Title,
		Decision:     d.Decision,
		DeciderID:    d.DeciderID,
		Context:      decodeJSONMap(strPtr(d.ContextJSON)),
		Rationale:    nonNilSlice(decodeStringSlice(strPtr(d.RationaleJSON))),
		Alternatives: nonNilSlice(decodeStringSlice(strPtr(d.AlternativesJSON))),
		CreatedAt:    d.CreatedAt,
	}
}

func attestationResponse(a domain.Attestation) AttestationResponse {
	return AttestationResponse{
		ID:         a.ID,
		ProjectID:  a.ProjectID,
		EntityKind: a.EntityKind,
		EntityID:   a.EntityID,
		Kind:       a.Kind,
		ActorID:    a.ActorID,
		TS:         a.TS,
		Payload:    decodeJSONMap(strPtr(a.PayloadJSON)),
	}
}

func eventResponse(e domain.Event) EventResponse {
	return EventResponse{
		ID:         e.ID,
		TS:         e.TS,
		Type:       e.Type,
		ProjectID:  e.ProjectID,
		EntityKind: e.EntityKind,
		EntityID:   e.EntityID,
		ActorID:    e.ActorID,
		Payload:    decodeJSONMap(strPtr(e.Payload)),
	}
}

func leaseResponse(l domain.Lease) LeaseResponse {
	return LeaseResponse{
		TaskID:     l.TaskID,
		OwnerID:    l.OwnerID,
		AcquiredAt: l.AcquiredAt,
		ExpiresAt:  l.ExpiresAt,
	}
}

func configResponse(cfg *config.Config) ProjectConfigResponse {
	res := ProjectConfigResponse{
		Project: projectConfigSection{
			ID:             cfg.Project.ID,
			TaskTypes:      map[string]taskTypeConfigResponse{},
			IterationTypes: map[string]iterationTypeConfigResponse{},
			Attestations:   []attestationConfigResponse{},
			RBAC: rbacConfigResponse{
				Permissions: map[string][]string{},
				Roles:       map[string]rbacRoleResponse{},
			},
		},
	}
	for name, tt := range cfg.Project.TaskTypes {
		policies := map[string]policyRuleResponse{}
		for pname, rule := range tt.Policies {
			policies[pname] = policyRuleResponse{All: nonNilSlice(rule.All)}
		}
		res.Project.TaskTypes[name] = taskTypeConfigResponse{Policies: policies}
	}
	for name, it := range cfg.Project.IterationTypes {
		policies := map[string]policyRuleResponse{}
		for pname, rule := range it.Policies {
			policies[pname] = policyRuleResponse{All: nonNilSlice(rule.All)}
		}
		res.Project.IterationTypes[name] = iterationTypeConfigResponse{Policies: policies}
	}
	for _, att := range cfg.Project.Attestations {
		res.Project.Attestations = append(res.Project.Attestations, attestationConfigResponse{
			ID:          att.ID,
			Category:    att.Category,
			Description: att.Description,
		})
	}
	for name, perms := range cfg.Project.RBAC.Permissions {
		res.Project.RBAC.Permissions[name] = nonNilSlice(perms)
	}
	for roleID, role := range cfg.Project.RBAC.Roles {
		res.Project.RBAC.Roles[roleID] = rbacRoleResponse{
			Description: role.Description,
			Grants:      nonNilSlice(role.Grants),
			CanAttest:   nonNilSlice(role.CanAttest),
		}
	}
	return res
}

// JSON helpers

func decodeJSONMap(raw *string) map[string]any {
	if raw == nil || *raw == "" {
		return nil
	}
	var tmp any
	if err := json.Unmarshal([]byte(*raw), &tmp); err != nil {
		return nil
	}
	if obj, ok := tmp.(map[string]any); ok {
		return obj
	}
	return nil
}

func decodeStringSlice(raw *string) []string {
	if raw == nil || *raw == "" {
		return []string{}
	}
	var arr []string
	if err := json.Unmarshal([]byte(*raw), &arr); err != nil {
		return []string{}
	}
	return arr
}

func nonNilSlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func strPtr(in string) *string {
	return &in
}
