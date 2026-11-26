# S0 — Foundations

**Weeks 1–2 · Gate G1 · Owner: platform engineer**

## Purpose

Produce a repository where every later stage can be built without renegotiating
anything: the module, the layout, the local stack, the CI that decides what "green"
means, and the small shared package every service depends on.

Nothing in this stage moves money. Everything in this stage decides how money will
be written for the next nine months.

## Depends on

Nothing. This is the first commit.

## Exit criterion

A new engineer clones the repository, runs one command, and gets: a running
PostgreSQL, NATS and Redis; a service binary that answers `/healthz` and `/readyz`;
a passing test suite; and a CI pipeline that reproduces that result from a clean
checkout. Demonstrated on a machine that has never built this project.

## Gate

**G1 — merge-ready.** The skeleton exists, CI is honest, and the constitution is in
force. See `docs/README.md` for the gate ladder.

## Why this stage exists

Because every hour spent here saves a day later. A service skeleton written once,
correctly, with tracing, configuration, graceful shutdown and a test harness, is the
difference between six services that behave the same way and six services that each
solved shutdown differently. The temptation is to skip ahead to the ledger because
the ledger is the interesting part. Doing that produces a ledger with no migration
tool, no observability and no way to test it, and the ledger is exactly the code that
must be tested.

## What you build

### The module and layout

- [ ] `go mod init github.com/donaina/echo` with Go 1.26 pinned in `go.mod` and a
      `toolchain` line.
- [ ] The layout from `README.md`: `cmd/`, `internal/`, `api/`, `db/`, `deploy/`, `docs/`.
- [ ] One `cmd/<service>/main.go` per service, each serving `/healthz` and `/readyz`
      on its assigned port. The services have no behaviour yet; they have a lifecycle.
- [ ] A `Makefile` with `build`, `test`, `test-race`, `lint`, `verify`, `up`, `down`,
      `migrate`. `make verify` is what CI runs and what a reviewer runs.

### The platform package

`internal/platform`, dependency-free except for `log/slog`, `context` and the
standard library's tests. It is the only package every service may import.

- [ ] `config` — one pattern for reading configuration: environment variables
      parsed into a typed struct, with every field having a default and a validation
      method. No service reads `os.Getenv` directly.
- [ ] `errors` — a typed domain error carrying a stable code, an HTTP status, a
      problem+json mapping and a safe message. See `reference/errors.md`.
- [ ] `logging` — a `slog` handler configured from `config`, with `request_id` and
      `correlation_id` fields and a redaction helper for amounts and identifiers.
- [ ] `telemetry` — OpenTelemetry setup: a tracer, a meter, a Prometheus exporter on
      `/metrics`, and propagation that reads `traceparent`.
- [ ] `httpserver` — server construction with timeouts (read, write, idle), graceful
      shutdown on `SIGTERM` with a drain period, and middleware for request id,
      correlation id, recovery, access logging and metrics.
- [ ] `testkit` — helpers for tests: a Postgres container or template database, a
      NATS test server, a fake clock, and a request builder for HTTP tests.

### The local stack

- [ ] `deploy/compose/docker-compose.yml` with PostgreSQL 17, NATS (JetStream
      enabled), Redis. Named volumes so a restart does not lose data.
- [ ] One database per service created by an init script, with a distinct role per
      service. `wallet` cannot read `auth`'s database from day zero.
- [ ] A `deploy/compose/README.md` listing ports and how to reset the stack.

### Migrations

- [ ] `goose` wired into `make migrate` and into each service's startup check.
      A service refuses to serve if its database is behind the embedded migrations.
- [ ] `db/<service>/` holds that service's migrations only. No shared migration
      directory, ever.
- [ ] The first migration per service: an empty `schema_migrations` baseline plus
      whatever the service needs to serve `/readyz` (nothing, usually).

### CI

- [ ] A pipeline that runs on every pull request: `gofmt`/`goimports` check, `go vet`,
      `staticcheck`, unit tests, race tests, generated-code freshness (`sqlc`,
      `buf`), migration apply-from-empty.
- [ ] A cache for Go modules and build artefacts so the pipeline is under ten minutes.
- [ ] Required status checks on `main`; no direct pushes.

### The documents

- [ ] `docs/00-constitution.md` is in the repository and linked from the README.
- [ ] `CONTRIBUTING.md` is in force.
- [ ] The first ADRs exist: `0001-record-architecture-decisions`, `0002-go-and-postgres-per-service`,
      `0003-integer-minor-units`.

## Data model changes

None that matter. Each service gets its own empty database and its own migration
directory. No business tables.

## API surface

| Service | Endpoint | Behaviour |
|---|---|---|
| all | `GET /healthz` | Process is alive. 200 always, unless the process is broken |
| all | `GET /readyz` | Dependencies reachable and migrations applied. 503 otherwise |
| all | `GET /metrics` | Prometheus exposition, internal port only |

No business endpoints.

## Invariants this stage must leave true

- **Article IX** — the databases are already separate. Enforce it now, while it costs
  nothing, because later it will cost a rewrite.
- **Article XI** — the logging and tracing foundation exists; every request already
  has a `request_id` and a `correlation_id` before any business code assumes them.
- **Article XII** — CI is the evidence mechanism. If it does not exist yet, no later
  gate can be honest.

## Tests you must write

- [ ] A test that `config` rejects an invalid value and names the variable.
- [ ] A test that `httpserver` drains in-flight requests on shutdown rather than
      cutting them.
- [ ] A test that `errors` maps every code to a status and produces valid problem+json.
- [ ] A test that the migration tool applies from empty against a real Postgres.
- [ ] A test that `/readyz` returns 503 when the database is stopped, and 200 when
      it returns.

Nothing here has a business meaning. That is the point: the harness exists before the
thing it harnesses.

## Definition of done

- [ ] `make verify` passes from a clean checkout on macOS and Ubuntu.
- [ ] Seven services start, answer health, and shut down cleanly.
- [ ] Three ADRs are merged.
- [ ] The README's "Repository layout (target)" section matches the actual tree, or the
      difference is explained in the same pull request.
- [ ] A new engineer has completed the clone-and-run path and their friction is fixed.

## What NOT to build yet

- **Any ledger table.** Not even an empty `accounts`. The schema is Stage 1's job, and
  a guessed schema is worse than none.
- **Authentication.** `auth` is a health endpoint. Tokens are Stage 4.
- **The gateway's routing.** It serves health and nothing else.
- **A shared "models" package.** It is a lie about ownership and it will be deleted.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| CI passes locally, fails in CI | Time-dependent tests, local state | A flaky test in week one; fix it now, not later |
| Services cannot start without each other | Health checks that require all dependencies | `/readyz` returning 503 for a dependency the service does not use |
| Migrations "temporarily" shared | Convenience | Two services with write access to one database |
| The Makefile drifts from CI | CI defined separately | `make verify` and the pipeline do different things |

## Handover

Stage 1 inherits: a running skeleton, a test harness, a migration system, and a
constitution nobody has yet had reason to break. The first honest test of Stage 0 is
whether Stage 1 can add a table without asking a Stage 0 question.
