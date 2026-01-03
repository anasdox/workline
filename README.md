Proofline CLI (`pl`)
====================

Proofline stores all state in SQLite at `.proofline/proofline.db` and requires a project configuration file at `.proofline/proofline.yml`.

Initialization
--------------
- Run `pl init --project-id <id>` to create the workspace, database, and default config.
- Default config (editable) is generated with:
  - Attestation catalog entries for `ci.passed`, `review.approved`, `acceptance.passed`, `security.ok`, `iteration.approved`.
  - Policy presets: `low`, `medium`, `high`, `feature`, `bug`, `technical` as defined in the generated YAML.
  - Task defaults mapping task types to presets, and iteration validation requiring `iteration.approved`.
- `pl init` records events `project.init` and `config.created`.

Configuration
-------------
- `.proofline/proofline.yml` is the single source of truth for validation policies and attestation catalog.
- All commands load and validate this file; commands fail if it is missing or invalid (except `pl init` which creates it).
- Default policies are applied automatically on task creation based on `policies.defaults.task.<type>` unless overridden with `--policy` or explicit validation flags.
- Iteration validation uses `policies.defaults.iteration.validation.require`.

Testing
-------
Run `go test ./...` (use `GOMODCACHE`/`GOCACHE` env vars if needed for sandboxed environments).
