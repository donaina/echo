# Echo

A payments platform for the money plane: wallets, transfers, holds, a ledger that
cannot drift, a rail connection, and the seams that let a back office observe and
operate it without ever touching the money path.

Built from scratch. Not forked, not vendored. The design is informed by the
`dev-marker/payments` teaching codebase, but every decision here is made again and
written down.

> **Status: Stage 0.** The documentation set is the specification, and the skeleton
> now exists: seven services with a lifecycle, the shared platform package, CI, and a
> local stack with one database per service. Nothing moves money yet — the first
> ledger table is Stage 1's, deliberately. Each later stage produces working software
> that the previous stage's tests still pass against.

---

## What this is

A Go platform of seven services behind one public edge:

| Service | Role |
|---|---|
| `gateway` | The only public surface. Routing, token verification, rate limits, request shaping. Holds no money state. |
| `auth` | Registration, login, token issue and rotation, key management. Owns credentials and sessions. |
| `wallet` | Accounts, entries, balances, holds. **The authoritative money state.** |
| `transfer` | Orchestration across wallets and the rail. Owns transfer lifecycle and saga state; holds no balance. |
| `notification` | Email and SMS delivery, templates, retries, dead letters. |
| `webhook` | Partner subscriptions, signed delivery, retries, replay. |
| `recon` | Ledger integrity, payments-to-core and payments-to-rail reconciliation, break lifecycle. |

Supporting infrastructure: PostgreSQL 17 (one database per service, no shared schema),
NATS JetStream (events), Redis (rate limits and short-lived projections only — never
money), OpenTelemetry (traces, metrics, logs).

## What this is not

- Not a core banking system. It posts business operations to one; it does not become one.
- Not a workflow engine. The back office (TOVARYN) owns approvals and cases; it issues
  a narrow command API and reads an event feed.
- Not a rail certification. The NIP connection is built and proven against a simulator,
  then certified by the operator in its own process.

## The five refusals

These are architecture, not preference. Every one of them is enforced by a test or a
contract guard by the end of Stage 6.

1. **No float, no decimal string, no unit ambiguity.** Money is `int64` minor units.
2. **No stored balance treated as truth.** A balance is re-derivable from entries.
3. **No retry without an idempotency key.** A timeout is `UNKNOWN`, never `FAILED`.
4. **No dual write.** Events leave through the transactional outbox.
5. **No cross-service database access.** One owner per fact.

The full statement is [`docs/00-constitution.md`](docs/00-constitution.md). If a pull
request violates an article, the article wins.

## Build stages

Ten stages, each a running system, each with an exit criterion that can be demonstrated.

| Stage | Produces | Weeks |
|---|---|---|
| [S0 Foundations](docs/phases/00-foundations.md) | Repo, tooling, local stack, CI, the platform package | 1–2 |
| [S1 Ledger core](docs/phases/01-ledger-core.md) | Accounts, transactions, entries, derived balance | 2–3 |
| [S2 Money movement](docs/phases/02-money-movement.md) | Funding, withdrawal, internal transfer under concurrency | 2–3 |
| [S3 Idempotency and saga](docs/phases/03-idempotency-and-saga.md) | Idempotent writes, holds, compensation | 2–3 |
| [S4 Identity and edge](docs/phases/04-identity-and-edge.md) | Auth, tokens, the gateway | 2–3 |
| [S5 Distribution](docs/phases/05-distribution.md) | Outbox, NATS, notification, webhook | 2–3 |
| [S6 Assurance](docs/phases/06-assurance.md) | Test tiers, race, fuzz, load, observability, SLOs | 2–3 |
| [S7 Core posting](docs/phases/07-core-posting.md) | Posting adapter, status enquiry, daily reconciliation | 3–4 |
| [S8 Rail](docs/phases/08-rail.md) | NIP: enquiry, transfer, status, holds, settlement | 3–4 |
| [S9 Hub and pilot](docs/phases/09-hub-and-pilot.md) | Event feed, command API, hardening, pilot | 4–6 |

Sequential effort is roughly six to nine months for four to six engineers. Stages
4 and 5 can overlap; nothing else can without building against a guess.

## Repository layout

```
cmd/          one main package per service
internal/     service code, never imported across services
  platform/   shared, dependency-free: config, errors, logging, telemetry, testkit
  ledger/     the ledger domain: accounts, entries, holds, invariants
api/          openapi/ for HTTP, proto/ for gRPC
db/           migrations per service, one directory each
deploy/       compose, helm, terraform
docs/         this documentation set
```

No `internal/pkg` grab-bag. No shared domain models between services — five small
structs beat one large one that lies about ownership.

## How to use the docs

- **New to the project?** Read [`docs/README.md`](docs/README.md), then the constitution.
- **Building a stage?** Open its file. It is the scope; anything not in it is not in scope.
- **Reviewing a PR?** Check the constitution articles first, then the stage's
  definition of done.
- **Debugging an invariant?** [`docs/reference/ledger-model.md`](docs/reference/ledger-model.md).

## Source of truth

| Question | Answer lives in |
|---|---|
| What must never break | `docs/00-constitution.md` |
| What am I building this stage | `docs/phases/` |
| What does this table/field mean | `docs/reference/ledger-model.md`, `docs/reference/data-model.md` |
| What does this endpoint accept | `docs/reference/api.md` |
| What does this event carry | `docs/reference/events.md` |
| What does this error mean | `docs/reference/errors.md` |
| How is this tested | `docs/reference/testing.md` |
| What is measured, and what pages someone | `docs/reference/observability.md` |
| Who may call this, and with what | `docs/reference/security.md` |
| What runs where, and which core/rail version | `docs/reference/environments.md` |
| Why was this decided | `docs/adr/` |

When the code and a document disagree, the document is a bug too. Fix both in the
same pull request.
