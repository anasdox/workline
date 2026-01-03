package engine_test

import (
	"context"
	"testing"
	"time"

	"proofline/internal/config"
	"proofline/internal/db"
	"proofline/internal/domain"
	"proofline/internal/engine"
	"proofline/internal/migrate"
)

type testEnv struct {
	Engine engine.Engine
	Ctx    context.Context
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(db.Config{Workspace: dir})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := migrate.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := config.Default("proj-1")
	eng := engine.New(conn, cfg)
	eng.Now = func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	if _, err := eng.InitProject(ctx, "proj-1", "test", "tester"); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := eng.Repo.UpsertProjectConfig(ctx, "proj-1", cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return testEnv{Engine: eng, Ctx: ctx}
}

func TestTaskStatusTransitions(t *testing.T) {
	env := newTestEnv(t)
	task, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{
		ProjectID: "proj-1",
		Title:     "Do work",
		ActorID:   "tester",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	// valid path
	task, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "in_progress", ActorID: "tester", Force: true})
	if err != nil || task.Status != "in_progress" {
		t.Fatalf("to in_progress: %v", err)
	}
	task, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "review", ActorID: "tester", Force: true})
	if err != nil || task.Status != "review" {
		t.Fatalf("to review: %v", err)
	}
	task, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "done", ActorID: "tester", Force: true})
	if err != nil || task.Status != "done" {
		t.Fatalf("to done: %v", err)
	}
	// invalid transition should error
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "planned", ActorID: "tester"})
	if err == nil {
		t.Fatalf("expected transition error")
	}
}

func TestDependencyGating(t *testing.T) {
	env := newTestEnv(t)
	dep, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{ProjectID: "proj-1", Title: "dep", ActorID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{
		ProjectID: "proj-1", Title: "main", ActorID: "tester", DependsOn: []string{dep.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// move task to review
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "review", ActorID: "tester", Force: true})
	// attempt done should fail while dependency not done
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "done", ActorID: "tester"})
	if err == nil {
		t.Fatalf("expected dependency blocking")
	}
	// finish dependency then allow
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: dep.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: dep.ID, Status: "review", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: dep.ID, Status: "done", ActorID: "tester", Force: true})
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "done", ActorID: "tester", Force: true})
	if err != nil {
		t.Fatalf("expected done after deps complete: %v", err)
	}
}

func TestSubtaskGating(t *testing.T) {
	env := newTestEnv(t)
	parent, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{ProjectID: "proj-1", Title: "parent", ActorID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{ProjectID: "proj-1", Title: "child", ActorID: "tester", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: parent.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: parent.ID, Status: "review", ActorID: "tester", Force: true})
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: parent.ID, Status: "done", ActorID: "tester"})
	if err == nil {
		t.Fatalf("expected parent blocked by child")
	}
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: child.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: child.ID, Status: "review", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: child.ID, Status: "done", ActorID: "tester", Force: true})
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: parent.ID, Status: "done", ActorID: "tester", Force: true})
	if err != nil {
		t.Fatalf("expected parent done after child: %v", err)
	}
}

func TestLeaseClaimRelease(t *testing.T) {
	env := newTestEnv(t)
	env.Engine.Now = time.Now
	task, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{ProjectID: "proj-1", Title: "lease", ActorID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := env.Engine.ClaimLease(env.Ctx, task.ID, "tester", 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if lease.OwnerID != "tester" {
		t.Fatalf("unexpected owner")
	}
	// claiming again by other actor before expiry fails
	_, err = env.Engine.ClaimLease(env.Ctx, task.ID, "other", 1)
	if err == nil {
		t.Fatalf("expected lease held error")
	}
	// wait for expiry
	time.Sleep(1100 * time.Millisecond)
	_, err = env.Engine.ClaimLease(env.Ctx, task.ID, "other", 1)
	if err != nil {
		t.Fatalf("expected claim after expiry: %v", err)
	}
	if err := env.Engine.ReleaseLease(env.Ctx, task.ID, "tester"); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestPolicyEvaluation(t *testing.T) {
	env := newTestEnv(t)
	tk, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{
		ProjectID:      "proj-1",
		Title:          "policy",
		ActorID:        "tester",
		ValidationMode: "all",
		RequiredKinds:  []string{"ci.passed", "review.approved"},
		PolicyOverride: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// try move to done without attestations should fail
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: tk.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: tk.ID, Status: "review", ActorID: "tester", Force: true})
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: tk.ID, Status: "done", ActorID: "tester"})
	if err == nil {
		t.Fatalf("expected validation failure")
	}
	// add attestations
	_, err = env.Engine.AddAttestation(env.Ctx, domain.Attestation{
		ProjectID:  "proj-1",
		EntityKind: "task",
		EntityID:   tk.ID,
		Kind:       "ci.passed",
	}, "tester")
	if err != nil {
		t.Fatalf("att1: %v", err)
	}
	_, err = env.Engine.AddAttestation(env.Ctx, domain.Attestation{
		ProjectID:  "proj-1",
		EntityKind: "task",
		EntityID:   tk.ID,
		Kind:       "review.approved",
	}, "tester")
	if err != nil {
		t.Fatalf("att2: %v", err)
	}
	_, err = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: tk.ID, Status: "done", ActorID: "tester", Force: true})
	if err != nil {
		t.Fatalf("expected done after attestations: %v", err)
	}
}

func TestAttestationEventLogged(t *testing.T) {
	env := newTestEnv(t)
	att, err := env.Engine.AddAttestation(env.Ctx, domain.Attestation{
		ProjectID:  "proj-1",
		EntityKind: "project",
		EntityID:   "proj-1",
		Kind:       "init.check",
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if att.ID == "" {
		t.Fatalf("expected attestation id")
	}
	rows, err := env.Engine.DB.QueryContext(env.Ctx, `SELECT count(*) FROM events WHERE entity_kind='attestation'`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var count int
	rows.Next()
	rows.Scan(&count)
	if count == 0 {
		t.Fatalf("expected event rows")
	}
}

func TestEventAppendOnStateChanges(t *testing.T) {
	env := newTestEnv(t)
	task, err := env.Engine.CreateTask(env.Ctx, engine.TaskCreateOptions{ProjectID: "proj-1", Title: "evented", ActorID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "in_progress", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "review", ActorID: "tester", Force: true})
	_, _ = env.Engine.UpdateTask(env.Ctx, engine.TaskUpdateOptions{ID: task.ID, Status: "done", ActorID: "tester", Force: true})
	rows, err := env.Engine.DB.QueryContext(env.Ctx, `SELECT type FROM events WHERE entity_id=?`, task.ID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if count < 3 {
		t.Fatalf("expected multiple events, got %d", count)
	}
}
