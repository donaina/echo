# S7 — Core posting

**Weeks 15–18 · Gate G3 · Owner: integration engineer, ledger engineer**

## Purpose

Connect the payments ledger to the book of record. Every completed money operation
that the core must know about becomes a posting: a business operation sent with a
stable reference, retried until acknowledged, and reconciled daily so that the two
ledgers cannot drift apart quietly.

This is seam B. It is the first time the platform talks to something outside itself
that owns money.

## Depends on

S3 (saga and `Unknown`), S5 (outbox and events), S6 (the guarantees that make a daily
reconciliation trustworthy).

## Exit criterion

A day of synthetic movement — 10,000 operations across 500 accounts — posts to the
core, and a reconciliation run reports zero unexplained breaks. Then one operation is
deliberately made to time out at the core after applying; the platform marks it
`Unknown`, resolves it by status enquiry, and the reconciliation confirms both ledgers
agree. Then one operation is removed from the core side by hand; the reconciliation
detects it, opens a break, and the break is closed with evidence.

## Gate

**G3 — synthetic release readiness.** The seam is proven in all three directions:
success, unknown, and divergence.

## Why this stage exists

Because a payments ledger that is not reflected in the bank's books is a shadow
ledger, and a shadow ledger is a liability with a nice API. The core is where the
institution's actual position lives. Posting makes the platform's money real; the
reconciliation makes it trustworthy.

## What you build

### The posting contract

The platform sends **business operations**, never GL legs. Which accounts to debit
and credit in the general ledger is the core's decision, made under the
institution's chart of accounts. The platform says what happened and for how much.

```json
{
  "operation_id":     "01J...",          // the payments ULID; unique forever
  "idempotency_key":  "01J...",          // stable across retries; the core must dedupe on it
  "operation_type":   "fund-wallet",     // closed list, below
  "account_ref":      "acct_...",        // the payments-side account this concerns
  "core_account_ref": "000123456",       // the core-side account, resolved at onboarding
  "amount_minor":     250000,
  "currency":         "NGN",
  "value_date":       "2026-11-24",
  "reference_number": "ECHO-20261124-0007X3",
  "narration":        "Wallet funding",
  "entries": [
    { "role": "customer", "direction": "credit", "amount_minor": 250000 },
    { "role": "funding-source", "direction": "debit", "amount_minor": 250000 }
  ]
}
```

- **`operation_type`** is a closed list, shared with the ledger and the ecosystem
  documents: `fund-wallet`, `withdraw-wallet`, `internal-transfer`, `fee`,
  `fx-settlement`, `reversal`. Adding one is an ADR and a version bump.
- **`reference_number`** is generated once per operation, by the payments side,
  persisted on the transaction, and never regenerated. It is the key the core stores
  and the reconciliation matches on.
- **`entries`** are a business split — the roles the money moved between — not ledger
  codes. This is what lets the adapter map to any core's chart of accounts without the
  payments side knowing it.

### The adapter

```go
// internal/posting/port
type Poster interface {
    // Post sends one operation. It must be idempotent on idempotency_key.
    Post(ctx context.Context, op Operation) (PostResult, error)
    // Enquire reports the outcome of an operation whose Post result was unknown.
    Enquire(ctx context.Context, referenceNumber string) (PostResult, error)
}

const (
    Posted PostStatus = iota
    Rejected
    Unknown
)
```

- [ ] `fineract` — implementation against the Apache Fineract API. Maps operation types
      to Fineract transaction types and finds or creates the client/savings account
      mapping. Documented in `reference/api.md` under "core adapter".
- [ ] `localcba` — implementation against the local NestJS CBA's posting endpoint,
      which is the first target for the pilot.
- [ ] `mock` — deterministic, used in tests; can be told to time out after applying.
- [ ] The adapter is selected by config; exactly one is active per deployment.
- [ ] Every adapter call is wrapped in a timeout **shorter than** the saga's, so the
      saga always sees a decision rather than hanging.

### Posting as an outbox-driven step

Posting does not run inside the request. It runs from the saga, and its own retries
are persisted.

- [ ] `postings` table: `id`, `transaction_id`, `operation_id`, `reference_number`,
      `status` (`pending`/`posted`/`rejected`/`unknown`/`resolved-posted`/
      `resolved-rejected`), `attempts`, `next_attempt_at`, `core_ref`, `last_error`.
- [ ] Created in the same transaction as the ledger entry, so a posted transaction
      always has a posting row.
- [ ] A worker posts due rows with backoff; `Unknown` is retried through `Enquire`
      rather than `Post`, because re-posting an unknown operation is exactly what
      idempotency keys exist to make safe — but enquiry is cheaper and tells the truth.
- [ ] A posting that stays `Unknown` beyond the policy limit opens a break and alerts;
      it is never silently dropped.
- [ ] `Rejected` by the core is a **critical** condition: the ledger has money the core
      refused. It opens an immediate break, freezes the affected account, and pages.

### The mapping

- [ ] `account_mappings`: `payments_account_id`, `core_account_ref`, `core_product`,
      `created_at`, `status`. Populated during onboarding; an account without a
      mapping cannot post, and the operation fails closed.
- [ ] Onboarding is a documented procedure: create the core account, record the
      mapping, post an opening balance as a `fund-wallet` from the opening-balance
      system account.

### The reconciliation service (`recon`)

The seventh service, and the reason it exists as a separate service rather than a job.

- [ ] **Daily run** at a configured cut-off: for each account, compare
      payments-ledger net movement for the business date with the core's booked
      movement. Match on `reference_number` first, then on `(account, date, amount)`.
- [ ] **Intraday run** for high-value operations only (threshold in config): every
      15 minutes, reconcile operations above the threshold that are older than 5
      minutes.
- [ ] **Ledger integrity run**: every hour, recompute balances from entries and
      compare with checkpoints and with the API's reported balances.
- [ ] `recon_runs`: `id`, `kind`, `business_date`, `started_at`, `finished_at`,
      `matched`, `unmatched_payments`, `unmatched_core`, `unexplained`.
- [ ] `breaks`: `id`, `run_id`, `kind`, `severity`, `account_ref`, `reference_number`,
      `amount_minor`, `status`, `assigned_to`, `opened_at`, `resolved_at`, `evidence`.

### The break lifecycle

Detect → Classify → Assign → Investigate → Resolve → Prove. Each transition is a row,
audited.

| Class | Meaning | Typical resolution |
|---|---|---|
| `missing-in-core` | Payments has it, core does not | Post the operation again with the same reference |
| `missing-in-payments` | Core has it, payments does not | Investigate; usually an out-of-band core entry |
| `amount-mismatch` | Both have it, different amount | Never auto-resolve; investigate |
| `duplicate-in-core` | Core booked twice | Reverse one in the core, with evidence |
| `unknown-stale` | A posting stuck in `Unknown` | Resolve by enquiry; close or reverse |

- [ ] Nobody resolves a break by editing a ledger. Resolution is a new, attributed
      operation, or a proven match.
- [ ] A break has an owner and a deadline by severity; overdue breaks are on the
      daily operations page.

## Data model changes

wallet: `postings`, `account_mappings`. recon: `recon_runs`, `breaks`, `break_events`.

## API surface

| Method | Path | Notes |
|---|---|---|
| GET | `/v1/recon/runs` | Operator: list runs |
| GET | `/v1/recon/runs/{id}` | Run summary and break counts |
| GET | `/v1/recon/breaks` | Filter by status, severity, account |
| POST | `/v1/recon/breaks/{id}/assign` | Operator |
| POST | `/v1/recon/breaks/{id}/resolve` | Operator, requires evidence |
| POST | `/v1/recon/breaks/{id}/note` | Append to the case |

## Invariants this stage must leave true

- **Article X** — the platform never reads a balance from the core to serve a
  customer, and never posts per-entry. It posts operations and reconciles.
- **Article VII** — a core timeout is `Unknown`; resolution is by enquiry, never by
  assumption.
- **Article IV** — a rejected posting is never fixed by mutating the ledger entry; it
  is fixed by a reversal and a new operation.
- **Article XI** — every break has an owner and a transition history.

## Tests you must write

- [ ] **Idempotent post** — the same operation posted twice yields one core booking
      (assert against the mock and against a real Fineract in the integration suite).
- [ ] **Unknown → posted** — mock applies then times out; enquiry finds it; status
      becomes `resolved-posted`; no second booking.
- [ ] **Unknown → rejected** — mock times out without applying; enquiry says rejected;
      the saga compensates; the ledger reverses.
- [ ] **Rejected is critical** — a rejection opens a break, freezes the account, pages.
- [ ] **Recon match** — a full day of synthetic movement reconciles with zero
      unexplained.
- [ ] **Recon detects tampering** — remove one core booking; the run finds
      `missing-in-core`; resolution posts it again with the same reference.
- [ ] **Duplicate detection** — book twice in the core; the run finds
      `duplicate-in-core`.
- [ ] **Break lifecycle** — every transition is recorded and a resolved break cannot be
      reopened without a new evidence row.

## Definition of done

- [ ] The three exit-criterion demonstrations are recorded and linked.
- [ ] `reference/api.md` documents the posting contract and the core mapping rules.
- [ ] The recon run is scheduled, monitored, and its result is on the daily operations
      dashboard.
- [ ] A runbook exists for `posting-rejected`, `break-overdue`, and `recon-run-failed`.
- [ ] Fineract integration is verified against the deployed version, and the version is
      recorded in `reference/environments.md`.

## What NOT to build yet

- **The rail.** S8. The rail posts through this same adapter, so building it now would
  duplicate a seam not yet proven.
- **Automated break resolution.** Tempting and dangerous. A break is closed by a human
  with evidence until the pilot shows which classes are truly mechanical.
- **Bidirectional sync from core to payments.** Payments is not a mirror of the core;
  it is the operational ledger in front of it.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Money in payments, absent from core | Posting rejected and swallowed | A `rejected` posting with no break |
| Double booking in the core | Reference regenerated on retry | `duplicate-in-core` breaks rising |
| Recon never matches | Value date vs posting date confusion | `unmatched_payments` ≈ `unmatched_core` every day |
| Breaks pile up unowned | No assignment rule | `breaks` open without `assigned_to` |

## Handover

Stage 8 adds the second external counterparty: the rail. It reuses the posting
adapter's shape, the unknown outcome, and the reconciliation service — which is why
the rail is cheaper to build than it looks.
