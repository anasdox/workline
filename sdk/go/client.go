package worklinesdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a minimal Workline HTTP API client.
type Client struct {
	BaseURL     string
	ProjectID   string
	APIKey      string
	BearerToken string
	HTTPClient  *http.Client
	Timeout     time.Duration
}

// New creates a client with sane defaults.
func New(baseURL, projectID string) *Client {
	return &Client{
		BaseURL:   baseURL,
		ProjectID: projectID,
		Timeout:   10 * time.Second,
	}
}

// Task represents the API task model (partial).
type Task struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Status    string `json:"status"`
}

// Attestation represents a proof entry.
type Attestation struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	EntityKind string         `json:"entity_kind"`
	EntityID   string         `json:"entity_id"`
	Kind       string         `json:"kind"`
	ActorID    string         `json:"actor_id"`
	Payload    map[string]any `json:"payload,omitempty"`
	TS         string         `json:"ts"`
}

// Event represents a log entry.
type Event struct {
	ID         int64          `json:"id"`
	TS         string         `json:"ts"`
	Type       string         `json:"type"`
	ProjectID  string         `json:"project_id"`
	EntityID   string         `json:"entity_id"`
	EntityKind string         `json:"entity_kind"`
	Payload    map[string]any `json:"payload"`
}

// Validation represents a validation artifact.
type Validation struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	TaskID    string   `json:"task_id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Issues    []string `json:"issues"`
	URL       string   `json:"url"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// ActorProfile represents an actor's mission and capabilities.
type ActorProfile struct {
	ProjectID    string   `json:"project_id"`
	ActorID      string   `json:"actor_id"`
	Mission      string   `json:"mission"`
	Actions      []string `json:"actions"`
	Attestations []string `json:"attestations"`
	Roles        []string `json:"roles"`
}

// APIError wraps non-2xx responses.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error: status=%d body=%s", e.StatusCode, e.Body)
}

// PaginatedEvents wraps list responses with cursors.
type PaginatedEvents struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor"`
}

// CreateTask creates a task.
func (c *Client) CreateTask(ctx context.Context, title, taskType string) (Task, error) {
	body := map[string]any{
		"title": title,
		"type":  taskType,
	}
	var resp Task
	err := c.do(ctx, http.MethodPost, c.projectPath("tasks"), body, &resp)
	return resp, err
}

// AddAttestation adds a proof.
func (c *Client) AddAttestation(ctx context.Context, entityKind, entityID, kind string, payload any) (Attestation, error) {
	body := map[string]any{
		"entity_kind": entityKind,
		"entity_id":   entityID,
		"kind":        kind,
		"payload":     payload,
	}
	var resp Attestation
	err := c.do(ctx, http.MethodPost, c.projectPath("attestations"), body, &resp)
	return resp, err
}

// Events returns recent events.
func (c *Client) Events(ctx context.Context, limit int) ([]Event, error) {
	page, err := c.EventsPage(ctx, limit, "")
	return page.Items, err
}

// EventsPage returns a paginated event listing.
func (c *Client) EventsPage(ctx context.Context, limit int, cursor string) (PaginatedEvents, error) {
	endpoint := c.projectPath("events")
	if limit > 0 {
		endpoint = fmt.Sprintf("%s?limit=%d", endpoint, limit)
	}
	if cursor != "" {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		endpoint = fmt.Sprintf("%s%scursor=%s", endpoint, sep, url.QueryEscape(cursor))
	}
	var resp PaginatedEvents
	err := c.do(ctx, http.MethodGet, endpoint, nil, &resp)
	return resp, err
}

// ActorProfile returns the mission, actions, and attestations for an actor.
func (c *Client) ActorProfile(ctx context.Context, actorID string) (ActorProfile, error) {
	var resp ActorProfile
	endpoint := c.projectPath(fmt.Sprintf("actors/%s/profile", url.PathEscape(actorID)))
	err := c.do(ctx, http.MethodGet, endpoint, nil, &resp)
	return resp, err
}

// CreateValidation creates a validation artifact for a task.
func (c *Client) CreateValidation(ctx context.Context, taskID, kind, status, summary string, issues []string, artifactURL string) (Validation, error) {
	body := map[string]any{
		"kind":    kind,
		"status":  status,
		"summary": summary,
		"issues":  issues,
		"url":     artifactURL,
	}
	var resp Validation
	endpoint := c.projectPath(fmt.Sprintf("tasks/%s/validations", url.PathEscape(taskID)))
	err := c.do(ctx, http.MethodPost, endpoint, body, &resp)
	return resp, err
}

// ListValidations returns validations for a task.
func (c *Client) ListValidations(ctx context.Context, taskID string) ([]Validation, error) {
	var resp struct {
		Items []Validation `json:"items"`
	}
	endpoint := c.projectPath(fmt.Sprintf("tasks/%s/validations", url.PathEscape(taskID)))
	err := c.do(ctx, http.MethodGet, endpoint, nil, &resp)
	return resp.Items, err
}

// GetValidation fetches a validation by id.
func (c *Client) GetValidation(ctx context.Context, id string) (Validation, error) {
	var resp Validation
	endpoint := c.projectPath(fmt.Sprintf("validations/%s", url.PathEscape(id)))
	err := c.do(ctx, http.MethodGet, endpoint, nil, &resp)
	return resp, err
}

// UpdateValidation updates a validation artifact.
func (c *Client) UpdateValidation(ctx context.Context, id, kind, status, summary string, issues []string, artifactURL string) (Validation, error) {
	body := map[string]any{
		"kind":    kind,
		"status":  status,
		"summary": summary,
		"issues":  issues,
		"url":     artifactURL,
	}
	var resp Validation
	endpoint := c.projectPath(fmt.Sprintf("validations/%s", url.PathEscape(id)))
	err := c.do(ctx, http.MethodPatch, endpoint, body, &resp)
	return resp, err
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, out any) error {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.Timeout}
	}
	url := c.base() + "/" + strings.TrimLeft(endpoint, "/")
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case c.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	case c.APIKey != "":
		req.Header.Set("X-Api-Key", c.APIKey)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &APIError{StatusCode: resp.StatusCode, Body: string(b)}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) projectPath(p string) string {
	project := url.PathEscape(c.ProjectID)
	return fmt.Sprintf("v0/projects/%s/%s", project, strings.TrimLeft(p, "/"))
}

func (c *Client) base() string {
	return strings.TrimRight(c.BaseURL, "/")
}
