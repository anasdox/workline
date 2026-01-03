package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	"proofline/internal/config"
	"proofline/internal/db"
	"proofline/internal/domain"
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
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	testSrv := &testServer{
		URL:    "http://" + ln.Addr().String(),
		client: &http.Client{},
		close: func() {
			srv.Shutdown(context.Background())
			ln.Close()
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
	if createRes.StatusCode != http.StatusOK {
		t.Fatalf("create task status %d: %s", createRes.StatusCode, string(data))
	}
	var created domain.Task
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
		if res.StatusCode != http.StatusOK {
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
	var fetched domain.Task
	_ = json.Unmarshal(taskBody, &fetched)

	doneRes, doneBody := doJSON(t, client, http.MethodPost, srv.URL+"/v0/projects/"+projectID+"/tasks/"+taskID+"/done?force=true", map[string]any{
		"work_proof": map[string]any{"note": "ok"},
	}, nil)
	if doneRes.StatusCode != http.StatusOK {
		t.Fatalf("done status %d: %s", doneRes.StatusCode, string(doneBody))
	}
	var done domain.Task
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
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create task: %d %s", res.StatusCode, string(data))
	}
	var created domain.Task
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
	if res.StatusCode != http.StatusOK {
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
