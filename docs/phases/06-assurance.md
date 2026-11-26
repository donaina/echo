# S6 — Assurance

**Weeks 13–15 · Gate G3 · Owner: QA engineer, SRE, ledger engineer**

## Purpose

Prove the platform survives the conditions the pilot will actually produce: sustained
load, a lying network, a dying process, and a balance derived from a million entries.
Then make every one of those conditions visible before a customer reports it.

Nothing new is built here. Everything already built is measured, and the weak parts
are fixed.

## Depends on

S1–S5. The full feature surface, through delivery.

## Exit criterion

Three things, demonstrated:

1. A load test sustains 200 TPS of internal transfers for 30 minutes with p99 under
   400 ms and zero balance divergence.
2. A chaos run kills the transfer worker, the relay, and the NATS connection in
   sequence during the same load test; no transfer is lost, no transfer is duplicated,
   and every saga reaches a terminal state.
3. Balance derivation over an account with 5 million entries returns in under 50 ms,
   using verified checkpoints, and the integrity job reports zero unexplained
   divergence.

## Gate

**G3 — synthetic release readiness.** The system is measured, and its failures are
instrumented.

## Why this stage exists

Because correctness under test conditions is not correctness. A ledger that is right
when nothing goes wrong is a ledger that will be wrong in its first incident. S6 is
the stage that converts "we think it works" into numbers, and the numbers become the
SLOs the pilot is judged against.

## What you build

### The test taxonomy

Five tiers, each with a home and a cost. A test lives in exactly one.

| Tier | Runs against | Speed | Purpose |
|---|---|---|---|
| Unit | nothing | ms | Domain rules, state machines, pure functions |
| Property | nothing (or in-memory fake) | ms | Invariants over generated inputs — zero-sum, idempotency, saga legality |
| Integration | real Postgres, real NATS | seconds | Schema constraints, transactions, relay, consumers |
| Contract | recorded interactions | seconds | Consumer expectations against producer schemas |
| End-to-end | full compose stack | minutes | A user journey: register → fund → transfer → webhook |

- [ ] `make test` runs unit and property.
- [ ] `make test-integration` runs integration.
- [ ] `make test-e2e` runs the full stack.
- [ ] CI runs all three on every pull request, with integration and e2e on a
      container runner.
- [ ] Coverage is reported but not gated at a percentage. The gate is that every
      article in the constitution has at least one named test (the constitution's
      enforcement column).

### Race and fuzzing

- [ ] `go test -race` on every package in CI, not only on the money packages.
- [ ] Fuzz targets with a CI time budget and corpus checked in:
      - amount and currency parsing (the edge between strings and `int64`)
      - idempotency fingerprint canonicalisation
      - JWT header and claim parsing
      - webhook signature verification
      - the saga state machine's transition function (an illegal transition must be
        rejected, never panic)
- [ ] A fuzz run of 10 minutes per target nightly; failures become regression tests.

### Load

- [ ] A load harness (k6 or `vegeta`) in `deploy/load/` with three profiles:
      - **steady** — 200 TPS internal transfers, 30 minutes
      - **spike** — 20 TPS to 400 TPS in 30 seconds, hold 2 minutes, drop
      - **read-heavy** — 1,000 TPS balance and entry reads against one account
- [ ] Targets, written down before the run: internal transfer p99 ≤ 400 ms; balance
      read p99 ≤ 30 ms; `available_balance` correct under all profiles.
- [ ] Each run asserts **invariants as well as latency**: post-run, sum of entries
      equals expected movement, no negative balances, no open holds belonging to
      terminal transfers.
- [ ] The harness reports a run id, and results are stored per run for comparison.

### Balance checkpoints

The one performance feature in this stage, and the one the ecosystem documents warn
about. A derived balance is correct; it is also O(history). The answer is not a
hand-maintained balance column — that is the Fineract failure — but a **verified
checkpoint**.

```sql
CREATE TABLE balance_checkpoints (
  id               TEXT PRIMARY KEY,
  account_id       TEXT NOT NULL REFERENCES accounts(id),
  as_of_entry_id   TEXT NOT NULL,         -- boundary; entries strictly after this are added
  as_of_created_at TIMESTAMPTZ NOT NULL,
  balance_minor    BIGINT NOT NULL,       -- exact balance through the boundary, inclusive
  entry_count      BIGINT NOT NULL,
  verified_at      TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX balance_checkpoints_account_boundary_uq
  ON balance_checkpoints (account_id, as_of_entry_id);
```

- [ ] `EntryBalance` becomes: latest checkpoint for the account (or zero) plus
      `SUM(delta_minor)` over entries after the boundary. Still correct; no longer
      O(history).
- [ ] A checkpoint is created only by a job that **locks the account row**, computes
      the exact balance from entries, and writes the boundary in the same transaction.
      A checkpoint is therefore exact at creation.
- [ ] **Verification:** an integrity job recomputes from scratch and compares. It runs
      for: every account daily, every checkpoint older than seven days with a full
      recompute, and a random 1% sample hourly. A mismatch deletes the checkpoint and
      opens a break — the checkpoint is a cache, and a wrong cache is removed, not
      repaired.
- [ ] Checkpoints are created at a lagging boundary (the entry id at the time of the
      job), never at the tip, so a checkpoint can never race a concurrent posting.
- [ ] A test asserts that derivation with checkpoints equals derivation from zero for
      random ledgers, with checkpoints inserted at random points.

This is the durable answer to the large-history problem: exact derivation, bounded by
checkpoints, with the checkpoints themselves verified. No unverified cache anywhere.

### Observability

- [ ] **Metrics** (RED per endpoint, plus domain):
      request rate, errors, duration histograms; `transfer_sagas_by_state`;
      `holds_open`; `outbox_oldest_unpublished_seconds`; `nats_consumer_lag`;
      `webhook_delivery_failure_rate`; `ledger_integrity_breaks_open`;
      `idempotency_replays_total`; `queue_depth` for the saga worker.
- [ ] **Traces:** every HTTP request and every saga step spans, linked by
      `correlation_id`. A transfer's full path — gateway → wallet → saga → remote — is
      one trace.
- [ ] **Logs:** structured, correlated; a money operation logs its outcome and its
      `transaction_id`, never its amount.
- [ ] **Dashboards:** one per service plus one for the money path. The money-path
      dashboard is the one that is on a screen during the pilot.
- [ ] **Alerts on symptoms, not causes.** "Transfer p99 > 1s for 5 minutes" alerts.
      "CPU > 80%" does not; it pages nobody at 3am because it is not a user problem.

### SLOs

| SLO | Target | Window |
|---|---|---|
| Internal transfer success (excluding client errors) | 99.9% | 30 days |
| Internal transfer p99 latency | ≤ 400 ms | 30 days |
| Balance read p99 | ≤ 30 ms | 30 days |
| Event delivery lag p99 | ≤ 5 s | 30 days |
| Webhook delivery within 5 minutes | ≥ 99% | 30 days |
| Unexplained ledger divergence | 0 | always |

- [ ] Each SLO has a documented error budget and an alert at 50% and 100% burn.
- [ ] A runbook entry per alert in `deploy/runbooks/`, each with: what it means, first
      three checks, escalation.

### Chaos

- [ ] Scenarios, each runnable in CI nightly and by hand:
      - kill the saga worker during sustained load; assert at-least-once and no loss
      - kill the relay; assert no event loss and bounded lag
      - drop the NATS connection for 60 s; assert services keep serving money
      - pause Postgres for 30 s; assert requests fail fast with `503`, not hang
      - inject 2 s latency into the remote destination; assert `Unknown` and resolution
      - skew the clock by ±5 minutes on one node; assert token and webhook validation
        behave per the documented tolerance
- [ ] Each scenario has a pass condition written before it is run. A chaos run that
      "seemed fine" is not evidence.

## Data model changes

`balance_checkpoints`. No change to existing tables.

## API surface

No new endpoints. Existing endpoints gain documented SLOs and error semantics.

## Invariants this stage must leave true

- **Article II** — a checkpoint is a cache, verified or deleted. There is still no
  authoritative stored balance.
- **Article VI** — chaos proves no event is lost and no event is invented.
- **Article VII** — chaos proves an unknown outcome parks and resolves, never guesses.
- **Article XII** — the exit criterion is evidence. The numbers above are the evidence.

## Tests you must write

The tests are the stage. Specifically:

- [ ] All five tiers exist with the make targets above.
- [ ] The invariant test sweep: a run that exercises every constitution article and
      names the test that enforces it.
- [ ] Checkpoint equivalence property test.
- [ ] Chaos pass conditions as automated assertions.
- [ ] SLO burn-rate alert tests (a synthetic burn triggers the alert).

## Definition of done

- [ ] The three exit-criterion runs are recorded with their results committed to
      `docs/reference/testing.md` or a linked run log.
- [ ] Every alert has a runbook.
- [ ] The money-path dashboard exists and is reviewed by someone who did not build it.
- [ ] `reference/observability.md` and `reference/testing.md` are written and accurate.
- [ ] A post-mortem-style "what we found" note exists, listing the defects S6 caught —
      this is the evidence that the stage was worth doing.

## What NOT to build yet

- **Autoscaling.** The pilot's volume is known; size for it and measure. Autoscaling
  adds failure modes that need their own assurance.
- **Multi-region.** Single region until the pilot proves demand. The write path is
  single-writer by design and multi-region makes that a two-year project.
- **A generic chaos platform.** Five scenarios, scripted, are enough. A platform is a
  distraction from the pilot.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Load passes but the ledger diverges | Only latency is asserted | Post-run invariant check missing |
| Chaos "passes" because nothing ran | No automated pass condition | A chaos run with no assertion |
| Checkpoints drift | Written outside the account lock | Integrity job reports frequent deletions |
| Alert fatigue in week one | Alerts on causes rather than symptoms | On-call ignoring pages |
| Coverage gamed | Percentage gate | High coverage, constitution articles untested |

## Handover

Stage 7 connects the platform to the book of record. It inherits a system that is
measured, whose failure modes are documented, and whose delivery guarantees are
known — which is exactly what a posting adapter and a daily reconciliation need in
order to be trusted.
