package domain

type Project struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at" format:"date-time"`
}

type Iteration struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Goal      string `json:"goal"`
	Status    string `json:"status" enum:"pending,running,delivered,validated,rejected"`
	CreatedAt string `json:"created_at" format:"date-time"`
}

type Task struct {
	ID                       string   `json:"id"`
	ProjectID                string   `json:"project_id"`
	IterationID              *string  `json:"iteration_id,omitempty"`
	ParentID                 *string  `json:"parent_id,omitempty"`
	Type                     string   `json:"type"`
	Title                    string   `json:"title"`
	Description              string   `json:"description,omitempty"`
	Status                   string   `json:"status" enum:"planned,ready,in_progress,review,done,rejected,canceled"`
	AssigneeID               *string  `json:"assignee_id,omitempty"`
	Priority                 *int     `json:"priority,omitempty"`
	WorkOutcomesJSON         *string  `json:"work_outcomes_json,omitempty"`
	RequiredAttestationsJSON *string  `json:"required_attestations_json,omitempty"`
	DependsOn                []string `json:"depends_on,omitempty"`
	CreatedAt                string   `json:"created_at" format:"date-time"`
	UpdatedAt                string   `json:"updated_at" format:"date-time"`
	CompletedAt              *string  `json:"completed_at,omitempty" format:"date-time"`
}

type Decision struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	Title            string `json:"title"`
	ContextJSON      string `json:"context_json,omitempty"`
	Decision         string `json:"decision"`
	RationaleJSON    string `json:"rationale_json,omitempty"`
	AlternativesJSON string `json:"alternatives_json,omitempty"`
	DeciderID        string `json:"decider_id"`
	CreatedAt        string `json:"created_at"`
}

type Lease struct {
	TaskID     string `json:"task_id"`
	OwnerID    string `json:"owner_id"`
	AcquiredAt string `json:"acquired_at" format:"date-time"`
	ExpiresAt  string `json:"expires_at" format:"date-time"`
}

type Attestation struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	EntityKind  string `json:"entity_kind"`
	EntityID    string `json:"entity_id"`
	Kind        string `json:"kind"`
	ActorID     string `json:"actor_id"`
	TS          string `json:"ts" format:"date-time"`
	PayloadJSON string `json:"payload_json,omitempty"`
}

type Event struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts" format:"date-time"`
	Type       string `json:"type"`
	ProjectID  string `json:"project_id,omitempty"`
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id,omitempty"`
	ActorID    string `json:"actor_id"`
	Payload    string `json:"payload_json"`
}

type APIKey struct {
	ID        string `json:"id"`
	ActorID   string `json:"actor_id"`
	Name      string `json:"name,omitempty"`
	KeyHash   string `json:"key_hash"`
	CreatedAt string `json:"created_at" format:"date-time"`
}

type ActorMission struct {
	ProjectID string `json:"project_id"`
	ActorID   string `json:"actor_id"`
	Mission   string `json:"mission"`
	CreatedAt string `json:"created_at" format:"date-time"`
	UpdatedAt string `json:"updated_at" format:"date-time"`
}

type ActorProfile struct {
	ProjectID    string   `json:"project_id"`
	ActorID      string   `json:"actor_id"`
	Mission      string   `json:"mission,omitempty"`
	Actions      []string `json:"actions"`
	Attestations []string `json:"attestations"`
	Roles        []string `json:"roles"`
}

type Validation struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	TaskID    string   `json:"task_id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Summary   string   `json:"summary,omitempty"`
	Issues    []string `json:"issues,omitempty"`
	URL       string   `json:"url,omitempty"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at" format:"date-time"`
	UpdatedAt string   `json:"updated_at" format:"date-time"`
}
