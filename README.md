Proofline CLI (`pl`)
====================

Why Proofline?
--------------
LLM agents that manage projects through text alone easily lose structure and context; Proofline gives them a typed API for tasks, iterations, attestations, and policies so they can read/write state safely instead of scraping checklists.
Too often, “done” is just a checkbox: tasks get marked complete without proof, policies are tribal knowledge, and quality gates are easy to forget. Proofline makes “done” and “ready” explicit by attaching attestations (proof) and enforcing policies automatically. It keeps evidence, rules, and history in one place so teams—and agents—ship with confidence instead of guesswork.


Proofline stores all state in SQLite at `.proofline/proofline.db`. Project configs (attestations + policies) live in the DB; you can import from `.proofline/proofline.yml` if you want to override defaults.

Core Concepts (explained simply)
--------------------------------
- Why attestations and policy-driven validation matter: they keep "done" honest. Attestations are proof stickers (tests passed, review approved); policies say which stickers are required before a task or iteration can finish, so quality is enforced automatically instead of by memory.
- Workspace: the `.proofline/` folder is your toy box; it holds the database and `proofline.yml` rules every command uses. Example: running `pl init --project-id myproj` builds this box.
- Project: the one big game you are playing in this workspace. Everything—iterations, tasks, evidence—belongs to this project.
- Policy presets (`policies.presets`): ready-made rules that say which proof is needed. Think: "before dessert you must finish veggies." Example: preset `high` might require `ci.passed`, `review.approved`, and `security.ok`.
- Definition of Ready (DoR): proof that a task is ready to start (e.g., `requirements.accepted`, `design.reviewed`, `scope.groomed`). Use the `ready` preset to gate work.
- Definition of Done (DoD): proof that a task is really done (e.g., `ci.passed`, `review.approved`, `acceptance.passed`). Task types map to DoD presets by default.
- Tasks: the pieces of work (feature, bug, doc). They can depend on others or have children. Status path is `planned -> in_progress -> review -> done` (with `rejected`/`canceled` side exits). Example: `pl task create --type feature --title "Login"` makes a new task; `pl task done <id> --work-proof-json '{}'` tries to finish it after checks.
- Iterations: short adventures inside the big game. Start `pending`, go `running`, then `delivered`, and finally `validated` when the right proof is present. Example: `pl iteration set-status iter-1 --status validated` requires the configured attestation unless `--force`.
- Attestations: proof stickers you attach to tasks or iterations (kinds live in the catalog). Example: after tests pass, add `pl attest add --entity-kind task --entity-id <id> --kind ci.passed`.
- Leases: a temporary "I’m working on this" tag so two kids don’t do the same task. Example: `pl task claim <id>` to grab, `pl task release <id>` to drop it.
- Event log: the diary of everything that happened. Example: `pl log tail --n 20` shows recent entries.

Build / Install
---------------
- Requirements: Go 1.22+
- Build: `go build ./...`
- Optional caches for sandboxed environments: prefix commands with `GOMODCACHE=$(pwd)/.gomodcache GOCACHE=$(pwd)/.gocache`.

Initialization
--------------
- Run `pl init --project-id <id>` to create the workspace, database, and default config.
- Default config (editable) is generated with:
  - Attestation catalog entries for readiness and done checks: `requirements.accepted`, `design.reviewed`, `scope.groomed`, `ci.passed`, `review.approved`, `acceptance.passed`, `security.ok`, `iteration.approved`.
  - Policy presets: `ready` (DoR), `done.standard`, `done.bugfix`, plus `low/medium/high`.
  - Task defaults map to DoD presets (`feature`→`done.standard`, `bug`→`done.bugfix`, etc.), and iteration validation requires `iteration.approved`.
- `pl init` records events `project.init` and `config.created`.

Configuration
-------------
- Project configs live in the DB. If no config exists for a project, a default is auto-seeded.
- You can import overrides from a YAML file: `pl project config import --file .proofline/proofline.yml`.
- Inspect/validate: `pl config show` and `pl config validate` (or `--json`).
- Project selection: `--project` overrides; else config file project_id if present; else the single project in DB. Config seeding happens only when the project has no stored config.
- Default policies are applied automatically on task creation based on `policies.defaults.task.<type>` unless overridden with `--policy` or explicit validation flags (`--validation-mode`, `--require`, `--threshold`), which emit `policy.override`.
- Iteration validation uses `policies.defaults.iteration.validation.require`; missing value means no attestation is required.

Quick Start
-----------
```sh
pl init --project-id myproj --description "Demo project"
pl config show
pl iteration create --id iter-1 --goal "Ship MVP"
pl task create --type feature --title "Implement auth"
pl task list
pl task claim <task-id>
pl task update <task-id> --status in_progress
pl attest add --entity-kind task --entity-id <task-id> --kind ci.passed
pl task done <task-id> --work-proof-json '{"notes":"implemented and tested"}'
pl log tail
```

Common Commands
---------------
- Status: `pl status`
- Tasks:
  - Create with policy preset: `pl task create --type feature --title "..." --policy high`
  - Update with preset: `pl task update <id> --set-policy medium`
  - Tree view: `pl task tree`
- Iterations:
  - Set status: `pl iteration set-status <id> --status validated`
- Attestations:
  - Add: `pl attest add --entity-kind iteration --entity-id iter-1 --kind iteration.approved`
  - List: `pl attest list --entity-kind task --entity-id <id>`
- Logs: `pl log tail --n 50`

HTTP API
--------
- Start server: `pl serve --addr 127.0.0.1:8080 --base-path /v0`.
- Base paths are project-scoped: `/v0/projects/{project_id}/tasks`, `/iterations`, `/attestations`, `/events`, `/status`. Projects: `POST/GET /v0/projects`, `GET/PATCH/DELETE /v0/projects/{project_id}`.
- OpenAPI spec: `http://127.0.0.1:8080/openapi.json`; Swagger UI: `http://127.0.0.1:8080/docs` (loads the generated spec, no static file).
- Authentication: use `Authorization: Bearer <JWT>` for humans or `X-Api-Key` for automation. `X-Actor-Id` is deprecated and ignored when auth headers are present (only honored if `ALLOW_LEGACY_ACTOR_HEADER=true`).
- Auth: none for v0; intended for local/agent use. Add auth before exposing beyond localhost.

SDKs
----
- Go: see `sdk/go` (package `prooflinesdk`). Quick start:
  ```go
  c := prooflinesdk.New("http://127.0.0.1:8080", "myproj")
  task, _ := c.CreateTask(context.Background(), "Ship feature", "feature")
  _, _ = c.AddAttestation(context.Background(), "task", task.ID, "ci.passed", nil)
  events, _ := c.Events(context.Background(), 10)
  fmt.Println("latest event", events[0].Type)
  ```
- Python: see `sdk/python/proofline.py`. Quick start:
  ```python
  from proofline import ProoflineClient
  c = ProoflineClient("http://127.0.0.1:8080", "myproj")
  task = c.create_task("Ship feature", "feature")
  c.add_attestation("task", task.id, "ci.passed")
  print(c.events(5)[0])
  ```

Agents (LangGraph / Autogen)
----------------------------
- LangGraph (Python) integration sketch:
  ```python
  from langgraph.graph import StateGraph
  from proofline import ProoflineClient

  client = ProoflineClient("http://127.0.0.1:8080", "myproj")

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
  from proofline import ProoflineClient

  client = ProoflineClient("http://127.0.0.1:8080", "myproj")

  assistant = AssistantAgent("assistant")
  user = UserProxyAgent("user", human_input_mode="NEVER")

  def add_task(title):
      task = client.create_task(title, "feature")
      client.add_attestation("task", task.id, "ci.passed")
      return f"created {task.id}"

  assistant.register_function(add_task, name="add_task", description="Create a task in Proofline")
  user_message = "Add a task to ship login"
  reply = assistant.run(user_proxy=user, prompt=user_message)
  print(reply)
  ```

Events and Policies
-------------------
- All state changes append to `events` (SQLite). Policy-related events include `task.policy.applied`, `task.policy.updated`, `policy.override`, and `iteration.validation.checked`.
- Validation decisions use the policy fields persisted on each task; presets from config populate these fields on create or when `--set-policy` is used.

Testing
-------
Run `go test ./...` (use `GOMODCACHE`/`GOCACHE` env vars if needed for sandboxed environments).

Contributing
------------
See `CONTRIBUTING.md` for coding standards, testing expectations, and PR checklist.

Notes
-----
- SDKs call the HTTP API; ensure `pl serve` is running and `--project` points to the right project. If you use a different base path, adjust `base_url` accordingly.
