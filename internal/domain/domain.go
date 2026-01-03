package domain

type Project struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type Iteration struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Goal      string `json:"goal"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type Task struct {
	ID                       string   `json:"id"`
	ProjectID                string   `json:"project_id"`
	IterationID              *string  `json:"iteration_id,omitempty"`
	ParentID                 *string  `json:"parent_id,omitempty"`
	Type                     string   `json:"type"`
	Title                    string   `json:"title"`
	Description              string   `json:"description,omitempty"`
	Status                   string   `json:"status"`
	AssigneeID               *string  `json:"assignee_id,omitempty"`
	WorkProofJSON            *string  `json:"work_proof_json,omitempty"`
	ValidationMode           string   `json:"validation_mode"`
	RequiredAttestationsJSON *string  `json:"required_attestations_json,omitempty"`
	RequiredThreshold        *int     `json:"required_threshold,omitempty"`
	DependsOn                []string `json:"depends_on,omitempty"`
	CreatedAt                string   `json:"created_at"`
	UpdatedAt                string   `json:"updated_at"`
	CompletedAt              *string  `json:"completed_at,omitempty"`
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
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at"`
}

type Attestation struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	EntityKind  string `json:"entity_kind"`
	EntityID    string `json:"entity_id"`
	Kind        string `json:"kind"`
	ActorID     string `json:"actor_id"`
	TS          string `json:"ts"`
	PayloadJSON string `json:"payload_json,omitempty"`
}

type Event struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts"`
	Type       string `json:"type"`
	ProjectID  string `json:"project_id,omitempty"`
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id,omitempty"`
	ActorID    string `json:"actor_id"`
	Payload    string `json:"payload_json"`
}
