Workline
========

Workline is an agent-first project manager: reliable storage (SQLite) plus a typed API for tasks, iterations, attestations, and policies. The goal is simple: replace fragile checklists with explicit proof (attestations) and verifiable rules (policies).

At a glance
-----------
- You create tasks and iterations.
- You attach proofs (tests OK, review OK, etc.).
- Policies automatically verify whether a task is truly "ready" or "done".
- Everything is recorded in an event log for traceability.

Why Workline
------------
AI agents and human teams lose context quickly: "done" becomes fuzzy, rules vary by person, and proof is scattered. Workline makes the workflow explicit:
- Proof attached to objects (attestations).
- Rules per task type (policies).
- A full history of decisions (events).

1 human + 1 AI = team. So project, governance, quality, maintainability. Workline covers that need: shared backlog, clear deliverables, workflow, validation, memory, protocols.

Where data lives
----------------
All state lives in SQLite at `.workline/workline.db`.
Configs (attestations + policies) are stored in the DB. You can import a sample from `workline.example.yml`.

Core concepts (pedagogical version)
----------------------------------
- Project: the main game in your workspace.
- Task: a unit of work (feature, bug, docs, etc.).
- Iteration: a slice of work (sprint, release).
- Attestation: proof (e.g. `ci.passed`, `review.approved`).
- Policy: a rule that requires attestations for a gate (ready, done, validation).
- DoR / DoD: definitions of "ready" / "done" based on proof.
- Event log: a journal of everything that changes.

Task lifecycle
--------------
Statuses: `planned -> in_progress -> review -> done` (with exits `rejected`/`canceled`).
Quick example:
```sh
wl task create --type feature --title "Login"
wl task claim <task-id>
wl task update <task-id> --status in_progress
wl attest add --entity-kind task --entity-id <task-id> --kind ci.passed
wl task done <task-id> --work-outcomes-json '{"notes":"implemented and tested"}'
```

Install / Build
---------------
- Requirements: Go 1.22+
- Build: `go build ./...`
- Optional caches (sandboxed environments):
  - `WORKLINE_GOMODCACHE=$(pwd)/.gomodcache`
  - `WORKLINE_GOCACHE=$(pwd)/.gocache`

Initialization
--------------
Nothing to run up front: the DB is created on first CLI use.
On first usage, Workline seeds:
- Base attestations: `requirements.accepted`, `design.reviewed`, `scope.groomed`, `ci.passed`, `review.approved`, `acceptance.passed`, `security.ok`, `iteration.approved`.
- Policies per task type: `project.task_types.<type>.policies`.
- Iteration validation: `project.iteration_types.standard.policies.validation`.
- RBAC: permissions, roles, and attestation capabilities.

Configuration
-------------
- Show / validate: `wl config show`, `wl config validate` (or `--json`).
- Project selection: `--project` or `WORKLINE_DEFAULT_PROJECT` (via `wl project use <id>`).
- Import a YAML file: `wl project config import --file workline.example.yml`.
- Policies per type: `project.task_types.<type>.policies` (gates `ready`, `done`).
- Iteration validation: `project.iteration_types.<name>.policies.validation`.

Quick Start
-----------
```sh
wl project config import --file workline.example.yml      # optional
wl project use myproj                                     # writes WORKLINE_DEFAULT_PROJECT to .env
wl config show
wl iteration create --id iter-1 --goal "Ship MVP"
wl task create --type feature --title "Implement auth"
wl task list
wl task claim <task-id>
wl task update <task-id> --status in_progress
wl attest add --entity-kind task --entity-id <task-id> --kind ci.passed
wl task done <task-id> --work-outcomes-json '{"notes":"implemented and tested"}'
wl log tail
```

Local bootstrap
---------------
- One-shot setup (deps + optional import): `./scripts/bootstrap.sh`
  - `WORKLINE_DEFAULT_PROJECT_CONFIG_FILE=workline.example.yml` to import
  - `WORKLINE_WORKSPACE` to override workspace
- With `just`: `just` (runs `bootstrap`), then `just test|fmt|tidy|serve`.

Useful commands
---------------
- Status: `wl status`
- Tasks:
  - Create with policy: `wl task create --type feature --title "..." --policy done`
  - Apply a policy: `wl task update <id> --set-policy done`
  - Tree view: `wl task tree`
- Iterations:
  - Set status: `wl iteration set-status <id> --status validated`
- Attestations:
  - Add: `wl attest add --entity-kind iteration --entity-id iter-1 --kind iteration.approved`
  - List: `wl attest list --entity-kind task --entity-id <id>`
- Logs: `wl log tail --n 50`

Roles and automation (agents)
-----------------------------
The file `workline.example.yml` includes roles `planner`, `executor`, `reviewer`.
Quick bootstrap (dev-only, bypasses RBAC):
```sh
wl rbac bootstrap --project myproj --actor planner-agent --role planner
wl rbac bootstrap --project myproj --actor executor-agent --role executor
wl rbac bootstrap --project myproj --actor reviewer-agent --role reviewer
```
Create API keys:
```sh
wl api-key create --actor planner-agent --name planner
wl api-key create --actor executor-agent --name executor
wl api-key create --actor reviewer-agent --name reviewer
```
Environment variables:
```sh
export WORKLINE_PLANNER_API_KEY=...
export WORKLINE_EXECUTOR_API_KEY=...
export WORKLINE_REVIEWER_API_KEY=...
```

HTTP API
--------
- Start: `wl serve --addr 127.0.0.1:8080 --base-path /v0` (uses `WORKLINE_DEFAULT_PROJECT`)
- Spec: `http://127.0.0.1:8080/openapi.json`
- Swagger UI: `http://127.0.0.1:8080/docs`
- Auth: `Authorization: Bearer <JWT>` for humans, `X-Api-Key` for automation.
- No auth on v0 (local use). Add auth before exposing externally.

SDKs
----
- Go: `sdk/go` (package `worklinesdk`).
```go
c := worklinesdk.New("http://127.0.0.1:8080", "myproj")
task, _ := c.CreateTask(context.Background(), "Ship feature", "feature")
_, _ = c.AddAttestation(context.Background(), "task", task.ID, "ci.passed", nil)
events, _ := c.Events(context.Background(), 10)
fmt.Println("latest event", events[0].Type)
```
- Python: `sdk/python/workline.py`.
```python
from workline import WorklineClient
c = WorklineClient("http://127.0.0.1:8080", "myproj")
task = c.create_task("Ship feature", "feature")
c.add_attestation("task", task.id, "ci.passed")
print(c.events(5)[0])
```

Agent integrations
------------------
- LangChain: `examples/langchain_workline.py`
- LangGraph:
```python
from langgraph.graph import StateGraph
from workline import WorklineClient

client = WorklineClient("http://127.0.0.1:8080", "myproj")

def create_and_mark_done(state):
    task = client.create_task(state["title"], "feature")
    client.add_attestation("task", task.id, "ci.passed")
    client.add_attestation("task", task.id, "review.approved")
    return {"task_id": task.id}

graph = StateGraph(dict)
graph.add_node("do_work", create_and_mark_done)
graph.set_entry_point("do_work")
result = graph.compile()({"title": "Ship feature"})
print(result)
```
- Autogen:
```python
from autogen import AssistantAgent, UserProxyAgent
from workline import WorklineClient

client = WorklineClient("http://127.0.0.1:8080", "myproj")

assistant = AssistantAgent("assistant")
user = UserProxyAgent("user", human_input_mode="NEVER")

def add_task(title):
    task = client.create_task(title, "feature")
    client.add_attestation("task", task.id, "ci.passed")
    return f"created {task.id}"

assistant.register_function(add_task, name="add_task", description="Create a task in Workline")
user_message = "Add a task to ship login"
reply = assistant.run(user_proxy=user, prompt=user_message)
print(reply)
```

Events and policies
-------------------
- Every change appends an event in SQLite.
- Key events: `task.policy.applied`, `task.policy.updated`, `policy.override`, `iteration.validation.checked`.
- Validation depends on policies stored on each task.

Webhooks
--------
- Workline can emit webhooks on events (config in `workline.example.yml`).
- Each webhook supports `url`, `events`, `secret`, `enabled`, `timeout_seconds`.
- Best-effort delivery: one event per POST, retried on next poll if non-2xx.

Tests
-----
`go test ./...` (use `WORKLINE_GOMODCACHE`/`WORKLINE_GOCACHE` if needed).

Contributing
------------
See `CONTRIBUTING.md` for standards, tests, and PR checklist.

Notes
-----
SDKs call the HTTP API. Make sure `wl serve` is running and `--project` is correct.
