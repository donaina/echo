# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repository is

Echo is a payments platform built one stage at a time. The documentation in `docs/` is the specification, not commentary: it is written before the code, and the code is measured against it.

- `docs/00-constitution.md` — twelve articles that must never be false. A change that breaks one is rejected, whatever feature it delivers.
- `docs/phases/00-foundations.md` … `09-hub-and-pilot.md` — the ten build stages. A phase document is the scope: work not in it is not in scope for that stage, and each has a "What NOT to build yet" section that is binding.
- `docs/reference/` — living documents: ledger model, data model, API, events, errors, testing, observability, security, environments.
- `docs/adr/` — accepted decisions, immutable once merged; supersede with a new ADR rather than editing.

Read the constitution before changing anything that touches money.

**Current state: Stages 0–2.** Seven services with health and readiness probes, the shared `internal/platform` package, one database and role per service, the ledger (accounts, transactions, entries, derived balances), and the wallet's internal HTTP API: fund, withdraw, internal transfer, wallet and entry reads. Holds, the public edge and the transfer saga are later stages and must not be guessed ahead of them.

## Commands

| Command | What it does |
|---|---|
| `make verify` | What CI runs: gofmt check, `go vet`, staticcheck, sqlc freshness, race tests, build |
| `make test` | Unit tests only; no Docker |
| `make test-race` | Unit tests under the race detector |
| `make test-integration` | Tests behind the `integration` build tag, against real containers |
| `make build` | Every service binary into `bin/`, stamped with `git describe` |
| `make up` | Stack, build, migrate, and run all seven services |
| `make logs` / `make down` / `make reset` | Follow logs; stop; destroy volumes and rebuild |
| `make migrate` | Apply every service's embedded migrations |
| `make generate` | Regenerate the sqlc wallet queries; `make verify` fails when they are stale |

A single test:

```bash
go test -run TestDrain ./internal/platform/httpserver
go test -tags=integration -run TestApplyFromEmpty ./internal/platform/migrate
```

`staticcheck` is pinned in `go.mod`, so it runs as `go tool staticcheck ./...` with nothing to install.

Integration tests are separated by build tag, not by `-short`: `make test` never needs Docker, and a test that needs a database says so in its build constraint.

## Architecture

Seven services in one module (`github.com/donaina/echo`), each with its own PostgreSQL database:

| Service | Owns |
|---|---|
| `gateway` | The only public surface: routing, token checks, rate limits. No database. |
| `auth` | Users, credentials, sessions. |
| `wallet` | Accounts, transactions, entries, holds — the authoritative money state. |
| `transfer` | Transfer lifecycle and saga state. Owns no balance. |
| `notification` | Email and SMS delivery state. |
| `webhook` | Partner subscriptions, signed deliveries, replay. |
| `recon` | Reconciliation runs and breaks. Owns no money movement. |

Services talk over the wire — HTTP, and gRPC where a contract justifies it — and over NATS JetStream. They never import each other.

`internal/platform` is the only shared code: `config`, `errors`, `logging`, `telemetry`, `httpserver`, `postgres`, `migrate`, `testkit`, and the `boundaries` test. It must not import a service, and the boundaries test enforces both directions of that rule.

### How a service is wired

`cmd/<service>/main.go` is wiring only: load configuration, connect, apply migrations when run with `-migrate`, verify the database is current, set up telemetry, register readiness checks, and serve until SIGTERM. `httpserver` owns the lifecycle every service shares: timeouts, request and correlation ids, tracing, panic recovery, access logging, metrics, probes, drain and graceful shutdown.

Each service embeds its migrations (`db/<service>/migrations.go` plus goose SQL files). A service refuses to start when its database is behind the binary's migrations; it never serves a schema older than the code that expects it.

### Invariants that shape the code

These are the constitution's articles where they affect daily work:

- Money is `int64` minor units with a currency. Never a float, a decimal, or an amount without its unit.
- A balance is derived from entries. Never add a stored balance column; performance is addressed with verified checkpoints, and only in S6.
- A debit is checked and written under the account's row lock. The lock, not the isolation level, is the guarantee (ADR 0009), and every entry writer — including a reversal — takes it, in ascending account-ID order.
- Every transaction's entries sum to zero per currency. The ledger is append-only; corrections are reversals. The database enforces both with triggers: passing the Go layer is not enough.
- Every money write is idempotent. A timeout is `Unknown`, never `Failed`; `internal/platform/errors` maps `context.DeadlineExceeded` to `outcome_unknown` and no code may collapse that into a retryable failure.
- Events leave through the transactional outbox. Never publish to NATS from a request path.
- One owner per fact: no cross-service database access, no shared tables, no shared domain models.

## Conventions

- **Errors**: return a typed `*errors.Error` built from the code enum in `internal/platform/errors`. Adding a code requires adding it to `docs/reference/errors.md` — a test fails otherwise.
- **Config**: no `os.Getenv` outside `internal/platform/config`. Read once at startup; fail with the variable's name. A value that could be wrong in production has no default.
- **Logs**: structured `slog` carrying `request_id` and `correlation_id`. No amounts, account numbers, tokens, or request bodies in log lines.
- **Metrics**: named `echo_<subsystem>_<name>_<unit>`. Labels stay bounded — route patterns, never raw paths or account ids.
- **Migrations**: goose, one directory per service, forward-only in production. Adding a non-null column to a large table is three migrations, not one.
- **Money writes**: a balance-checked movement goes through `withAccountLocks`; it is the only path that takes `FOR UPDATE`, and it locks in sorted order. Do not add another writer of entries that bypasses it.
- **Tests**: integration tests carry `//go:build integration` and use `internal/platform/testkit` for containers. Tests build state through the ledger; they never insert entries directly.
- **Dependencies**: the standard library first. A new dependency is a permanent cost and needs a reason in the pull request.

## Workflow

- **Never push to `main` and never merge a pull request.** Work goes on a branch, into a PR, and waits for Ayoola's approval. The one exception already in history is the empty bootstrap commit that created `main`.
- Conventional Commits, scoped by the directory that changed: `feat(wallet): …`, `fix(transfer): …`, `docs(adr): …`; `wip` is not a commit.
- Change the document in the same PR as the code. A change to the constitution also needs an ADR that explains what the old article got wrong.
- A stage closes only when its exit criterion is demonstrated — a passing test, a query result, a dashboard — never because the work feels done.

## Local stack

`make up` provides PostgreSQL 17 on 5432 (one database and role per service), NATS with JetStream on 4222 (monitoring 8222), and Redis on 6379. Service ports are HTTP/metrics: gateway 8080/9080, auth 8081/9081, wallet 8082/9082, transfer 8083/9083, notification 8084/9084, webhook 8085/9085, recon 8086/9086. The wallet's money API is on its internal port, 8182, behind `X-Echo-Service-Token` (`make up` defaults it to `local-dev-service-token`) and requires `X-Echo-Caller`. Only the gateway is public in production. `deploy/compose/README.md` has the local credentials and the reset procedure.

## Deliberately deferred

buf generated-code checks, golangci-lint, the contract and end-to-end test tiers, and the deadline-to-unknown lint rule arrive with the stages that need them. Do not add a gate that has nothing to check.
