package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proofline/internal/config"
	"proofline/internal/db"
	"proofline/internal/engine"
	"proofline/internal/migrate"
)

type testServer struct {
	URL    string
	client *http.Client
	close  func()
}

func (s *testServer) Client() *http.Client { return s.client }
func (s *testServer) Close()               { s.close() }

func newTestServer(t *testing.T) (*testServer, func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if strings.Contains(msg, "failed to listen") || strings.Contains(msg, "operation not permitted") {
				t.Skipf("skipping server tests in restricted environment: %v", r)
			}
			panic(r)
		}
	}()
	workspace := t.TempDir()
	if _, err := db.EnsureWorkspace(workspace); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	cfg := config.Default("proofline")
	conn, err := db.Open(db.Config{Workspace: workspace})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := migrate.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	e := engine.New(conn, cfg)
	if _, err := e.InitProject(context.Background(), cfg.Project.ID, "", "tester"); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := e.Repo.UpsertProjectConfig(context.Background(), cfg.Project.ID, cfg); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	handler, err := New(Config{Engine: e, BasePath: "/v0"})
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	ts := httptest.NewServer(handler)
	testSrv := &testServer{
		URL:    ts.URL,
		client: ts.Client(),
		close: func() {
			ts.Close()
			conn.Close()
		},
	}
	return testSrv, func() { testSrv.Close() }
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["X-Actor-Id"]; !ok && method != http.MethodGet {
		headers["X-Actor-Id"] = "tester"
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, data
}

func TestTaskDoneWithAttestations(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"

	client := srv.Client()
	createRes, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{
		"title": "Ship feature",
		"type":  "feature",
	}, nil)
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create task status %d: %s", createRes.StatusCode, string(data))
	}
	var created TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	taskID := created.ID

	for _, kind := range []string{"ci.passed", "review.approved", "acceptance.passed"} {
		res, body := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/attestations", map[string]any{
			"entity_kind": "task",
			"entity_id":   taskID,
			"kind":        kind,
		}, nil)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("attestation %s status %d: %s", kind, res.StatusCode, string(body))
		}
	}

	leaseRes, leaseBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+taskID+"/claim", nil, nil)
	if leaseRes.StatusCode != http.StatusOK {
		t.Fatalf("claim lease status %d: %s", leaseRes.StatusCode, string(leaseBody))
	}

	taskRes, taskBody := doJSON(t, client, http.MethodGet, srv.URL+"/v0/projects/"+projectID+"/tasks/"+taskID, nil, nil)
	if taskRes.StatusCode != http.StatusOK {
		t.Fatalf("get task status %d: %s", taskRes.StatusCode, string(taskBody))
	}
	var fetched TaskResponse
	_ = json.Unmarshal(taskBody, &fetched)

	doneRes, doneBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+taskID+"/done?force=true", map[string]any{
		"work_proof": map[string]any{"note": "ok"},
	}, nil)
	if doneRes.StatusCode != http.StatusOK {
		t.Fatalf("done status %d: %s", doneRes.StatusCode, string(doneBody))
	}
	var done TaskResponse
	if err := json.Unmarshal(doneBody, &done); err != nil {
		t.Fatalf("unmarshal done: %v", err)
	}
	if done.Status != "done" {
		t.Fatalf("expected status done, got %s", done.Status)
	}
}

func TestLeaseConflict(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	res, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{
		"title": "Lease me",
	}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create task: %d %s", res.StatusCode, string(data))
	}
	var created TaskResponse
	_ = json.Unmarshal(data, &created)

	claim1, body1 := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+created.ID+"/claim", nil, nil)
	if claim1.StatusCode != http.StatusOK {
		t.Fatalf("first claim: %d %s", claim1.StatusCode, string(body1))
	}
	claim2, body2 := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+created.ID+"/claim", nil, map[string]string{"X-Actor-Id": "other"})
	if claim2.StatusCode != http.StatusConflict {
		t.Fatalf("expected conflict, got %d %s", claim2.StatusCode, string(body2))
	}
}

func TestIterationValidationBlocked(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	res, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/iterations", map[string]any{
		"id":   "iter-1",
		"goal": "Test iteration",
	}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create iteration: %d %s", res.StatusCode, string(data))
	}

	runRes, runBody := doJSON(t, client, http.MethodPatch, srv.URL+"/v0/projects/"+projectID+"/iterations/iter-1/status", map[string]any{
		"status": "running",
	}, nil)
	if runRes.StatusCode != http.StatusOK {
		t.Fatalf("set running: %d %s", runRes.StatusCode, string(runBody))
	}

	deliveredRes, deliveredBody := doJSON(t, client, http.MethodPatch, srv.URL+"/v0/projects/"+projectID+"/iterations/iter-1/status", map[string]any{
		"status": "delivered",
	}, nil)
	if deliveredRes.StatusCode != http.StatusOK {
		t.Fatalf("set delivered: %d %s", deliveredRes.StatusCode, string(deliveredBody))
	}

	valRes, valBody := doJSON(t, client, http.MethodPatch, srv.URL+"/v0/projects/"+projectID+"/iterations/iter-1/status", map[string]any{
		"status": "validated",
	}, nil)
	if valRes.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected validation block (422), got %d %s", valRes.StatusCode, string(valBody))
	}
}

func TestCreateTaskValidation(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	res, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{}, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, string(data))
	}
	var apiErr struct {
		Error apiErrorBody `json:"error"`
	}
	_ = json.Unmarshal(data, &apiErr)
	if apiErr.Error.Code != "bad_request" {
		t.Fatalf("unexpected error code: %s", apiErr.Error.Code)
	}
}

func TestDoneTaskRequiresValidation(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	createRes, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{
		"title": "Needs validation",
		"type":  "feature",
	}, nil)
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create task: %d %s", createRes.StatusCode, string(data))
	}
	var task TaskResponse
	_ = json.Unmarshal(data, &task)

	claimRes, claimBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+task.ID+"/claim", nil, nil)
	if claimRes.StatusCode != http.StatusOK {
		t.Fatalf("claim lease: %d %s", claimRes.StatusCode, string(claimBody))
	}

	doneRes, doneBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+task.ID+"/done", map[string]any{
		"work_proof": map[string]any{"note": "testing"},
	}, nil)
	if doneRes.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", doneRes.StatusCode, string(doneBody))
	}
	var apiErr struct {
		Error apiErrorBody `json:"error"`
	}
	_ = json.Unmarshal(doneBody, &apiErr)
	if apiErr.Error.Code != "validation_failed" {
		t.Fatalf("unexpected error code: %s", apiErr.Error.Code)
	}
}

func TestConfigEndpoint(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	res, data := doJSON(t, client, http.MethodGet, srv.URL+"/v0/projects/"+projectID+"/config", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config status %d: %s", res.StatusCode, string(data))
	}
	var cfg ProjectConfigResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(cfg.Policies.Presets) == 0 || cfg.Policies.Defaults.Task["feature"] == "" {
		t.Fatalf("config missing presets/defaults: %+v", cfg)
	}
}

func TestValidationEndpoint(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	createRes, data := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{
		"title": "Validate me",
		"type":  "feature",
		"validation": map[string]any{
			"mode":    "all",
			"require": []string{"ci.passed", "review.approved"},
		},
	}, nil)
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create task: %d %s", createRes.StatusCode, string(data))
	}
	var task TaskResponse
	_ = json.Unmarshal(data, &task)

	attRes, attBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/attestations", map[string]any{
		"entity_kind": "task",
		"entity_id":   task.ID,
		"kind":        "ci.passed",
	}, nil)
	if attRes.StatusCode != http.StatusCreated {
		t.Fatalf("attestation status %d: %s", attRes.StatusCode, string(attBody))
	}

	valRes, valBody := doJSON(t, client, http.MethodGet, srv.URL+"/v0/projects/"+projectID+"/tasks/"+task.ID+"/validation", nil, nil)
	if valRes.StatusCode != http.StatusOK {
		t.Fatalf("validation status %d: %s", valRes.StatusCode, string(valBody))
	}
	var status ValidationStatusResponse
	if err := json.Unmarshal(valBody, &status); err != nil {
		t.Fatalf("unmarshal validation: %v", err)
	}
	if status.Satisfied {
		t.Fatalf("expected validation to be unsatisfied")
	}
	if len(status.Present) != 1 || len(status.Missing) != 1 {
		t.Fatalf("unexpected present/missing: %+v", status)
	}
}

func TestPaginationProvidesCursor(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()
	projectID := "proofline"
	client := srv.Client()

	for i := 0; i < 3; i++ {
		res, body := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks", map[string]any{
			"title": fmt.Sprintf("Task %d", i),
		}, nil)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create task %d: %d %s", i, res.StatusCode, string(body))
		}
	}

	res, data := doJSON(t, client, http.MethodGet, srv.URL+"/v0/projects/"+projectID+"/tasks?limit=1", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list tasks: %d %s", res.StatusCode, string(data))
	}
	var page paginatedTasks
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("unmarshal page: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatalf("expected next_cursor to be set")
	}
}
