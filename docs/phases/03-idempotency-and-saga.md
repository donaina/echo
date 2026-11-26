# S3 — Idempotency and saga

**Weeks 7–8 · Gate G2 · Owner: ledger engineer, service engineer**

## Purpose

Make money writes safe to retry, and make transfers that cannot be one database
transaction — because the destination is remote and might say no, or say nothing —
survive failure without losing or duplicating money.

Two mechanisms do it: idempotency keys at the edge, and a saga with holds and
compensation inside. They are built together because they are the same requirement
seen from two sides: *the client retried, and the money must not move twice.*

## Depends on

S2. Atomic movements and sorted locking.

## Exit criterion

A test drives a transfer whose destination is a stub that fails, hangs and then
succeeds. In every case the source wallet's money ends in exactly one of two states —
debited once, or never debited — and the hold table has no open rows at the end.
Separately, a test sends the same transfer request three times with one idempotency
key and asserts one transaction, one hold, one event.

## Gate

**G2 — integration-ready.** Retry safety and compensation are proven against injected
faults rather than hoped for.

## Why this stage exists

Because this is the stage the whole design exists for. A transfer that can be
described as "one database transaction" is a convenience; a transfer that crosses a
process boundary is the real problem, and it is unsolvable without accepting that some
outcomes are unknown. S3 is where the platform stops pretending otherwise.

## What you build

### Idempotency

```sql
CREATE TABLE idempotency_keys (
  id                    TEXT PRIMARY KEY,       -- ULID
  idempotency_key       TEXT NOT NULL,
  scope                 TEXT NOT NULL,          -- caller identity + route
  request_fingerprint   TEXT NOT NULL,          -- SHA-256 over method, path, canonical body
  status                TEXT NOT NULL,          -- 'in_progress' | 'completed'
  response_status       INT,
  response_body         JSONB,
  resource_id           TEXT,                   -- what was created, if anything
  created_at            TIMESTAMPTZ NOT NULL,
  expires_at            TIMESTAMPTZ NOT NULL,
  CONSTRAINT idempotency_status_chk CHECK (status IN ('in_progress','completed'))
);
CREATE UNIQUE INDEX idempotency_scope_key_uq ON idempotency_keys (scope, idempotency_key);
CREATE INDEX idempotency_expires_idx ON idempotency_keys (expires_at);
```

- [ ] Middleware `idempotent(scopeFn)` — required on every money endpoint. A request
      without `Idempotency-Key` is rejected with `400 IDEMPOTENCY_KEY_REQUIRED`.
- [ ] The algorithm:
      1. `INSERT ... ON CONFLICT DO NOTHING` with status `in_progress`.
      2. If inserted: execute the handler, then store the response and set
         `completed`, in the handler's own transaction where possible.
      3. If not inserted: read the existing row.
         - Fingerprint mismatch → `422 IDEMPOTENCY_KEY_REUSED` (the client reused a
           key for a different request — a bug on their side, and a safe refusal).
         - `completed` → replay the stored response verbatim, with
           `Idempotent-Replayed: true`.
         - `in_progress` → `409 REQUEST_IN_PROGRESS` (the first request is still
           running; the client should retry later, not now).
- [ ] Keys expire after 24 hours by default, configurable per route. Expiry is a
      scheduled delete, and the retention window is longer than the maximum client
      retry window by a documented factor (see `reference/api.md`).
- [ ] The fingerprint is computed over a **canonicalised** body: sorted keys, no
      insignificant whitespace, amounts as integers. A test asserts two spellings of
      the same JSON produce the same fingerprint.

### Holds

```sql
CREATE TABLE holds (
  id              TEXT PRIMARY KEY,
  account_id      TEXT NOT NULL REFERENCES accounts(id),
  amount_minor    BIGINT NOT NULL,
  currency        TEXT NOT NULL,
  reference       TEXT NOT NULL,
  status          TEXT NOT NULL,               -- 'open' | 'captured' | 'released' | 'expired'
  transaction_id  TEXT REFERENCES transactions(id), -- set on capture
  expires_at      TIMESTAMPTZ NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL,
  CONSTRAINT holds_amount_chk CHECK (amount_minor > 0),
  CONSTRAINT holds_status_chk CHECK (status IN ('open','captured','released','expired')),
  CONSTRAINT holds_capture_chk CHECK (status <> 'captured' OR transaction_id IS NOT NULL)
);
CREATE INDEX holds_open_account_idx ON holds (account_id) WHERE status = 'open';
```

- [ ] `available_balance = ledger_balance − SUM(open holds)`. The `GET /wallets/{id}`
      response now reports both, and they diverge whenever a hold exists.
- [ ] `Reserve` — creates a hold, but only if
      `ledger_balance − open holds ≥ amount`, checked **under the same account lock**
      as S2's movements. Two concurrent reserves cannot both succeed against the same
      money.
- [ ] `Capture(holdID, transactionID)` — marks the hold `captured` and links the
      transaction that realised it. Capturing a hold that is not `open` fails.
- [ ] `Release(holdID, reason)` — marks it `released`. Releasing twice is a no-op with
      a distinct return, not an error.
- [ ] Expiry — a sweeper releases holds past `expires_at`. Expiry is a safety net, not
      the primary path; see the failure note below.
- [ ] The sum of open holds for an account can never exceed its ledger balance. This is
      an assertion in the integrity job (and in S6, an hourly check).

A hold is not money movement. An open hold changes `available`, never `ledger_balance`,
and produces no entry. Only capture produces entries.

### The transfer saga

A transfer whose destination may be remote is a state machine persisted in
`transfer_sagas`, driven by a worker, not by the request goroutine.

```
Received
  → Validated          (currency, amount, participants exist)
  → Reserved           (hold at source; the only step that needs the source lock)
  → Debiting           (capture the hold: source entries written)
  → Crediting          (call the remote destination)
  → Completed          (or → Compensating)
```

- [ ] `transfer_sagas` table: `id`, `state`, `request` (JSONB), `idempotency_key_id`,
      `hold_id`, `attempts`, `next_attempt_at`, `last_error`, `remote_ref`,
      `created_at`, `updated_at`.
- [ ] State transitions are guarded: a transition is a conditional
      `UPDATE ... WHERE state = $expected`. A lost race is a retry, never a double
      transition.
- [ ] A worker polls due sagas with `FOR UPDATE SKIP LOCKED`, so several workers can
      run without contention.
- [ ] The request handler creates the saga and returns `202 Accepted` with the saga
      id. The client polls `GET /transfers/{id}`, or waits for the webhook (S5).

### The remote destination interface

The saga does not know what is on the other side. It knows an interface:

```go
type Destination interface {
    // Credit attempts to move money to the destination.
    // It must be safe to call more than once with the same reference.
    Credit(ctx context.Context, req CreditRequest) (CreditResult, error)
    // Enquiry reports what actually happened, for outcomes that were unknown.
    Enquiry(ctx context.Context, reference string) (CreditResult, error)
}

type CreditStatus int
const (
    Applied CreditStatus = iota
    Rejected
    Unknown          // ← first-class. Article VII.
)
```

- [ ] The interface lives in `internal/transfer/port` and has no implementation that
      knows about HTTP, NIP or the core.
- [ ] In this stage the only implementation is `stubDestination`, a test double that
      can be configured to apply, reject, hang, or return `Unknown` a configurable
      number of times. It exists to prove the saga, and it is deleted when S7 and S8
      provide real ones.

### Compensation

| Saga step that failed | Money state | Compensation |
|---|---|---|
| Validation | Nothing moved | Mark failed. Release the hold if one was taken. |
| Reserve | Nothing moved | Mark failed. No hold to release. |
| Capture (debit source) | Nothing moved (atomic) | Mark failed. Release the hold. |
| Credit — **Rejected** | Source debited | Reverse the debit into a new transaction; release or capture nothing. Mark failed. |
| Credit — **Applied** | Source debited, destination credited | Mark completed. |
| Credit — **Unknown** | Source debited, destination **may** be credited | **Do not compensate.** Park in `Resolving`. Poll `Enquiry`. Only a definitive answer moves it. |

The distinction between `Rejected` and `Unknown` is the whole of Article VII. A
rejected credit is safely reversed. An unknown credit is never reversed on a guess,
because the guess may be wrong in the direction that pays money out twice.

- [ ] `Resolving` state with exponential backoff on `Enquiry`, bounded by a policy
      that escalates to an operator after N attempts rather than looping forever.
- [ ] An operator command to resolve an exhausted saga by hand is S9's; until then the
      saga stays parked and the metric `transfer_sagas_unresolved` rises, which is
      correct.

### The outbox, written but not yet published

The saga writes its terminal event into `outbox` in the same database transaction as
its final state change. That satisfies Article VI now. The relay that publishes to
NATS, and the consumers, are S5.

```sql
CREATE TABLE outbox (
  id              TEXT PRIMARY KEY,
  aggregate_type  TEXT NOT NULL,   -- 'transfer'
  aggregate_id    TEXT NOT NULL,
  event_type      TEXT NOT NULL,   -- 'transfer.completed' | 'transfer.failed'
  payload         JSONB NOT NULL,
  headers         JSONB NOT NULL,  -- correlation_id, causation_id, schema_ref
  created_at      TIMESTAMPTZ NOT NULL,
  published_at    TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL;
```

## Data model changes

`idempotency_keys`, `holds`, `transfer_sagas`, `outbox`. Plus `available_balance` in
the wallet response, derived.

## API surface

| Method | Path | Change |
|---|---|---|
| POST | `/transfers` | New. Saga-backed. Requires `Idempotency-Key`. Returns `202` |
| GET | `/transfers/{id}` | New. Saga state and terminal outcome |
| GET | `/wallets/{id}` | Now returns `ledger_balance` and `available_balance` |
| POST | `/wallets/{id}/withdraw` | Now requires `Idempotency-Key` |
| POST | `/wallets/{id}/fund` | Now requires `Idempotency-Key` |
| POST | `/transfers/internal` | Now requires `Idempotency-Key`; still atomic |

## Invariants this stage must leave true

- **Article V** — every money write requires a key; every endpoint that has one
  returns a replayed response on retry.
- **Article VI** — terminal events are written to the outbox inside the state-change
  transaction.
- **Article VII** — `Unknown` is a distinct value with a distinct state and a resolver.
  There is no code path that maps a timeout to `Rejected`.
- **Article VIII** — `available` never exceeds `ledger`, holds have a lifecycle, and
  concurrent reserves cannot oversubscribe.

## Tests you must write

- [ ] **Idempotency replay** — same key, same body, three times → one transaction, the
      same response three times, `Idempotent-Replayed: true` on the second and third.
- [ ] **Idempotency conflict** — same key, different body → `422`, and no state change.
- [ ] **In-progress** — a slow handler and an immediate retry → `409`, and the first
      completes exactly once.
- [ ] **Concurrent reserves** — 50 goroutines reserve against a balance covering 20;
      exactly 20 holds are `open`.
- [ ] **Saga: rejected credit** — source debited, credit rejected, debit reversed,
      final ledger balance equals the opening balance, one `transfer.failed` event.
- [ ] **Saga: unknown credit** — source debited, credit unknown → parked, no
      reversal, `available` stays reduced until resolution.
- [ ] **Saga: unknown then applied** — the stub returns unknown once, then `Applied`
      on enquiry; the saga completes, the debit stands, exactly one destination credit.
- [ ] **Saga: crash mid-flight** — kill the worker between `Debiting` and `Crediting`;
      on restart the saga resumes without a second debit. (Drive this by asserting the
      state machine's resume from every state.)
- [ ] **Hold expiry** — an expired hold releases and `available` recovers.
- [ ] **No open holds at the end** — a property test over generated saga runs asserts
      the terminal state leaves no `open` holds for a failed transfer.

## Definition of done

- [ ] Every money endpoint rejects a request without `Idempotency-Key`.
- [ ] The saga state machine has a table-driven test covering every state transition,
      including illegal ones.
- [ ] `Unknown` appears in metrics, logs and the API response as its own outcome.
- [ ] `reference/ledger-model.md` documents holds; `reference/api.md` documents the
      idempotency contract and the 202 pattern.
- [ ] The stub destination is clearly marked as a test double with a deletion ticket in
      the S7 plan.

## What NOT to build yet

- **NATS publishing.** The outbox exists; the relay is S5. Do not wire a broker
  directly — that violates Article VI and will be deleted.
- **Operator resolution tooling.** A parked saga is acceptable at this stage. The
  console is S9.
- **Retry of the saga's remote credit beyond the framework's default.** The retry
  policy is a product decision about the destination, made in S7 and S8.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| A debit reversed after the destination actually credited | Timeout mapped to rejection | The `Unknown` metric is zero while timeouts are non-zero |
| Holds that never release | Sweeper not running, or capture wrote nothing | `holds_open` grows monotonically |
| Saga stuck in one state | Transition guarded on the wrong expected state | The `transfer_sagas_by_state` gauge flatlines |
| Duplicate credits at the destination | `Credit` not safe to call twice | The destination's own recon (S8) reports a ghost credit |

## Handover

Stage 4 opens the public edge. From this point a customer, not just a test, can drive
these endpoints — so authentication and authorisation are no longer deferred. The
saga, holds and idempotency are the reason that is safe.
