# Contributing

Two rules sit above style, taste and speed.

1. **Read [`docs/00-constitution.md`](docs/00-constitution.md) before your first pull
   request.** It states twelve properties that must never be false. A change that
   breaks one is rejected, no matter how useful it is.
2. **Change the document with the code.** If behaviour changes and a document now
   describes the old behaviour, the pull request is incomplete.

---

## Branching

`main` is always releasable. Everything else is short-lived.

```
main
 └─ feat/wallet-derive-balance
 └─ fix/outbox-relay-lag
 └─ docs/adr-0009-partitioning
```

- Branch from `main`, merge back within a week. Long-lived branches rot.
- Rebase rather than merge `main` into a branch.
- One logical change per pull request. A refactor plus a behaviour change is two PRs.

## Commits

Conventional Commits, scoped by service or package:

```
feat(wallet): derive available balance from holds instead of a column
fix(transfer): release the source hold when destination credit is rejected
docs(adr): record why the outbox is preferred to dual writes
test(ledger): assert zero-sum over generated transaction sequences
```

The scope is the directory that changed, not the stage. `chore`, `refactor`, `perf`
and `build` are allowed; `wip` is not — squash before asking for review.

## Pull request gates

A pull request cannot merge unless all of the following are true. This is the G2/G3
ladder from the programme gates, applied to day-to-day work.

| Check | What it means |
|---|---|
| `make verify` passes | format, vet, lint, unit tests, race detection, generated-code freshness |
| Tests fail without the change | A test that passes on `main` proves nothing about this PR |
| Constitution reviewed | The author states which articles the change touches and why it is safe |
| Migration reviewed | Every schema change has a forward migration and a note on reversibility |
| Interfaces reviewed | Any change to an event, endpoint or posting payload is a versioned change (see below) |
| No new dependency without a reason | A dependency is a permanent cost; say what it replaces |

### Breaking changes

Anything that crosses a service boundary — an event field, an HTTP response, a gRPC
message, a posting payload — is a contract. Contracts change only by:

- **Adding** an optional field, or
- **Deprecating** one with a sunset date and a dual-publish window, then removing it.

Renaming or removing a field in place is not allowed. If a consumer needs a different
shape, publish a new version. Nobody reads your release notes at 3am.

## Code rules

These are the mechanical parts of the constitution. They are checked in review and
increasingly by lint.

- **Money is `int64` minor units.** No `float64`, no `decimal` string, no `big.Rat`
  in a money path. A unit conversion happens exactly once, at the edge, and is named.
- **Time is UTC and stored as `timestamptz`.** A business date is a separate `date`
  field; do not overload one for the other.
- **Identifiers are ULIDs**, string-encoded, generated at the boundary of the service
  that owns the row.
- **Errors are typed.** A money path returns a domain error with a stable code, not
  `fmt.Errorf("something went wrong")`. See [`docs/reference/errors.md`](docs/reference/errors.md).
- **Context is threaded.** Every function that does I/O takes `ctx` first and honours
  it. No `context.Background()` below `main`.
- **No `panic` outside `main`.** A recovery middleware exists; it is not a design.
- **Logs are structured** (`slog`), carry `request_id` and `correlation_id`, and never
  contain an amount, an account number or a token.

### Package boundaries

```
cmd/<service>      wiring only: parse config, build deps, serve, shut down
internal/<service>  the service: transport, application, domain
internal/platform   shared and dependency-free: config, errors, logging, outbox,
                    telemetry, idempotency, testkit
```

- `internal/<a>` may not import `internal/<b>`. Services talk over the wire.
- `internal/platform` may not import any service.
- The domain layer may not import `pgx`, `nats` or `chi`. Persistence and transport are
  adapters behind interfaces the domain owns.

## Definition of done

A work item is done when all six are true. This mirrors the phase docs, so a stage
can only exit when its items are genuinely finished.

- [ ] The behaviour exists, behind a flag if it is not ready for traffic.
- [ ] A test fails if the behaviour regresses — property, integration or contract.
- [ ] A metric, trace or log makes it observable in production.
- [ ] A runbook entry exists for any new failure mode.
- [ ] Migrations apply from empty and re-apply cleanly in plan.
- [ ] The relevant document in `docs/` is updated.

## Writing an ADR

Decisions that are expensive to reverse get an ADR. That includes: choice of datastore,
identifier format, status semantics, retry policy, and anything a future engineer would
reasonably ask "why is it like this?" about.

```
docs/adr/0011-short-imperative-title.md
```

Copy [`docs/adr/0001-record-architecture-decisions.md`](docs/adr/0001-record-architecture-decisions.md)
as the template. Status starts as `Proposed`, becomes `Accepted` on merge, and is never
deleted — a superseded ADR links to its replacement.

## Reviewing

Review the invariants before the style. A pull request that is beautifully formatted
and quietly turns a timeout into a failure is worse than an ugly correct one. If you
cannot tell whether a money path is safe, ask for the test, not for an explanation.
