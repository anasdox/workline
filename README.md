Workline
====================

Why Workline?
--------------
AI agents that manage projects through text alone easily lose structure and context; Workline gives them a typed API for tasks, iterations, attestations, and policies so they can read/write state safely instead of scraping checklists.
Too often, “done” is just a checkbox: tasks get marked complete without proof, policies are tribal knowledge, and quality gates are easy to forget. Workline makes “done” and “ready” explicit by attaching attestations (proof) and enforcing policies automatically. It keeps evidence, rules, and history in one place so teams—and agents—ship with confidence instead of guesswork.

1 humain + 1 IA = equipe. Donc projet, donc management, donc gouvernance, donc tracabilite, donc qualite, donc maintenabilite. Workline couvre ce besoin: backlog partage, livrables clairs, workflow, validations, memoire, protocoles.

Workline stores all state in SQLite at `.workline/workline.db`. Project configs (attestations + policies) live in the DB; `workline.example.yml` shows a sample config you can import.

Core Concepts (explained simply)
--------------------------------

- Project: the one big game you are playing in this workspace. Everything—iterations, tasks, evidence—belongs to this project.
- Attestations: proof stickers you attach to tasks or iterations (kinds live in the catalog). Example: after tests pass, add `wl attest add --entity-kind task --entity-id <id> --kind ci.passed`.
- Policies: rules that say which attestation is needed for a given task type and gate. Think: "before dessert you must finish veggies." Example: `project.task_types.feature.policies.done` might require `ci.passed`, `review.approved`, and `acceptance.passed`.
- Definition of Ready (DoR): proof that a task is ready to start (e.g., `requirements.accepted`, `design.reviewed`, `scope.groomed`). Use a `ready` policy gate to block work until ready.
- Definition of Done (DoD): proof that a task is really done (e.g., `ci.passed`, `review.approved`, `acceptance.passed`). Task types carry their own `done` gate.
- Tasks: the pieces of work (feature, bug, docs, workshop, plan). They can depend on others or have children. Status path is `planned -> in_progress -> review -> done` (with `rejected`/`canceled` side exits). Example: `wl task create --type feature --title "Login"` makes a new task; `wl task done <id> --work-outcomes-json '{}'` tries to finish it after checks.
- Iterations: short adventures inside the big game. Start `pending`, go `running`, then `delivered`, and finally `validated` when the right proof is present. Example: `wl iteration set-status iter-1 --status validated` requires the configured attestation unless `--force`.
- Leases: a temporary "I’m working on this" tag so two kids don’t do the same task. Example: `wl task claim <id>` to grab, `wl task release <id>` to drop it.
- Event log: the diary of everything that happened. Example: `wl log tail --n 20` shows recent entries.

Build / Install
---------------
- Requirements: Go 1.22+
- Build: `go build ./...`
- Optional caches for sandboxed environments: set `WORKLINE_GOMODCACHE=$(pwd)/.gomodcache` and `WORKLINE_GOCACHE=$(pwd)/.gocache` (Makefile maps these to Go env vars).

Initialization
--------------
- Nothing to run up front. The database at `.workline/workline.db` is created on demand when you run a command.
- Initial config is seeded into the DB on first use with:
  - Attestation catalog entries for readiness and done checks: `requirements.accepted`, `design.reviewed`, `scope.groomed`, `ci.passed`, `review.approved`, `acceptance.passed`, `security.ok`, `iteration.approved`.
  - Task policies under `project.task_types.<type>.policies` (e.g., `ready`, `done`), and iteration validation under `project.iteration_types.standard.policies.validation`.
  - RBAC permissions and roles with `grants` and `can_attest`.

Configuration
-------------
- Project configs live in the DB. If no config exists for a project, a default is auto-seeded.
- You can import overrides from a YAML file: `wl project config import --file workline.example.yml` (or any file you choose).
- Inspect/validate: `wl config show` and `wl config validate` (or `--json`).
- Project selection: `--project` overrides; otherwise `WORKLINE_DEFAULT_PROJECT` is required (set via `wl project use <id>`). Config seeding happens only when the project has no stored config.
- RBAC config: define `project.rbac.permissions` sets and `project.rbac.roles` with `grants`. Attestation authority lives in `project.rbac.roles.<role>.can_attest`.
- Policies live under `project.task_types.<type>.policies` and are applied by name via `--policy` (e.g., `done`), or overridden with explicit required attestations (`--require`), which emits `policy.override`.
- Task types are defined in `project.task_types`.
- Iteration validation uses `project.iteration_types.<name>.policies.validation`; missing policy means no attestation is required.

Quick Start
-----------
```sh
wl project config import --file workline.example.yml      # optional: sync sample config into DB
wl project use myproj                                     # sets WORKLINE_DEFAULT_PROJECT in .env
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
- One-shot setup (deps + optional config import): `./scripts/bootstrap.sh` (set `WORKLINE_DEFAULT_PROJECT_CONFIG_FILE=workline.example.yml` to import; override workspace with `WORKLINE_WORKSPACE`).
- With `just` installed: `just` (runs `bootstrap` by default), then `just test|fmt|tidy|serve`.

Common Commands
---------------
- Status: `wl status`
- Tasks:
  - Create with policy: `wl task create --type feature --title "..." --policy done`
  - Update with policy: `wl task update <id> --set-policy done`
  - Tree view: `wl task tree`
- Iterations:
  - Set status: `wl iteration set-status <id> --status validated`
- Attestations:
  - Add: `wl attest add --entity-kind iteration --entity-id iter-1 --kind iteration.approved`
  - List: `wl attest list --entity-kind task --entity-id <id>`
- Logs: `wl log tail --n 50`

Automation + Roles (Agents)
---------------------------
- Roles in `workline.example.yml` include `planner`, `executor`, and `reviewer` for multi-agent setups.
- Bootstrap actors with roles (dev-only, bypasses RBAC checks):
  ```sh
  wl rbac bootstrap --project myproj --actor planner-agent --role planner
  wl rbac bootstrap --project myproj --actor executor-agent --role executor
  wl rbac bootstrap --project myproj --actor reviewer-agent --role reviewer
  ```
- Create API keys for automation (one per actor):
  ```sh
  wl api-key create --actor planner-agent --name planner
  wl api-key create --actor executor-agent --name executor
  wl api-key create --actor reviewer-agent --name reviewer
  ```
- Use the keys with `X-Api-Key` and set env vars for SDKs/examples:
  ```sh
  export WORKLINE_PLANNER_API_KEY=...
  export WORKLINE_EXECUTOR_API_KEY=...
  export WORKLINE_REVIEWER_API_KEY=...
  ```
- Inspect/revoke keys: `wl api-key list` and `wl api-key revoke --id <id>`.

HTTP API
--------
- Start server: `wl serve --addr 127.0.0.1:8080 --base-path /v0` (uses `WORKLINE_DEFAULT_PROJECT`; set `WORKLINE_JWT_SECRET`).
- Base paths are project-scoped: `/v0/projects/{project_id}/tasks`, `/iterations`, `/attestations`, `/events`, `/status`. Projects: `POST/GET /v0/projects`, `GET/PATCH/DELETE /v0/projects/{project_id}`.
- OpenAPI spec: `http://127.0.0.1:8080/openapi.json`; Swagger UI: `http://127.0.0.1:8080/docs` (loads the generated spec, no static file).
- Authentication: use `Authorization: Bearer <JWT>` for humans or `X-Api-Key` for automation. Legacy `X-Actor-Id` headers are no longer accepted.
- Auth: none for v0; intended for local/agent use. Add auth before exposing beyond localhost.

SDKs
----
- Go: see `sdk/go` (package `worklinesdk`). Quick start:
  ```go
  c := worklinesdk.New("http://127.0.0.1:8080", "myproj")
  task, _ := c.CreateTask(context.Background(), "Ship feature", "feature")
  _, _ = c.AddAttestation(context.Background(), "task", task.ID, "ci.passed", nil)
  events, _ := c.Events(context.Background(), 10)
  fmt.Println("latest event", events[0].Type)
  ```
- Python: see `sdk/python/workline.py`. Quick start:
  ```python
  from workline import WorklineClient
  c = WorklineClient("http://127.0.0.1:8080", "myproj")
  task = c.create_task("Ship feature", "feature")
  c.add_attestation("task", task.id, "ci.passed")
  print(c.events(5)[0])
  ```

Agents (LangGraph / Autogen)
----------------------------
- LangChain (Python) example: see `examples/langchain_workline.py`.
- LangGraph (Python) integration sketch:
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
- Autogen (Python) integration sketch:
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

Events and Policies
-------------------
- All state changes append to `events` (SQLite). Policy-related events include `task.policy.applied`, `task.policy.updated`, `policy.override`, and `iteration.validation.checked`.
- Validation decisions use the policy fields persisted on each task; presets from config populate these fields on create or when `--set-policy` is used.

Webhooks
--------
- Workline can emit webhooks for new events. Configure in `workline.example.yml` and import into the DB.
- Each webhook entry supports `url`, `events` (optional allowlist), `secret`, `enabled`, and `timeout_seconds`.
- Delivery is best-effort: in-memory cursor, one event per POST. Non-2xx responses are retried on the next poll.

Testing
-------
Run `go test ./...` (or set `WORKLINE_GOMODCACHE`/`WORKLINE_GOCACHE` env vars if needed for sandboxed environments).

Contributing
------------
See `CONTRIBUTING.md` for coding standards, testing expectations, and PR checklist.

Notes
-----
- SDKs call the HTTP API; ensure `wl serve` is running and `--project` points to the right project. If you use a different base path, adjust `base_url` accordingly.
