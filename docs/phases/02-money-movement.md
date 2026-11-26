# S2 — Money movement

**Weeks 5–6 · Gate G2 · Owner: ledger engineer, service engineer**

## Purpose

Put an HTTP face on the ledger and make the three movements that every wallet
needs — fund, withdraw, transfer between wallets — correct when two of them arrive
at the same instant.

This is the first stage where a user action can move money, and the first stage that
can overdraw an account if the concurrency story is wrong. The concurrency story is
most of the work.

## Depends on

S1. A ledger that posts, with exact derivation and no balance column.

## Exit criterion

A load test fires 200 concurrent withdrawals against an account funded for 100 of
them. Exactly 100 succeed, 100 are refused with `INSUFFICIENT_FUNDS`, the final
ledger balance is zero, and the sum of the 100 entries equals the opening balance.
No deadlocks, no negative balance, no retry storm.

## Gate

**G2 — integration-ready.** The movements are correct under concurrency and the
database enforces it, not only the service.

## Why this stage exists

Because "check the balance, then debit" is the single most common bug in payments
software, and it is invisible to the developer who writes it on a quiet machine.
Serialising it into its own stage forces the team to build the concurrent test that
proves it, before any other feature creates enough surface to hide the bug.

## What you build

### The write path

Every posting that depends on sufficiency follows the same shape:

1. Begin a database transaction.
2. Lock every account involved with `SELECT ... FOR UPDATE`, **in ascending `id`
   order**. Sorted locking is what prevents deadlock between two transfers going
   opposite ways.
3. Compute the derived balance inside the lock (or use a `MATERIALIZED` subquery in
   the same statement).
4. Decide: proceed, or return a typed refusal.
5. Post the transaction and its entries.
6. Commit.

- [ ] A helper `withAccountLocks(ctx, store, ids []string, fn func() error) error`
      that implements steps 1–6 and is the only way to write a conditional movement.
- [ ] It is a `SERIALIZABLE` transaction with bounded retry on serialization failure
      (three attempts, jittered backoff), because locks plus serializable is the
      belt-and-braces setting and the retry is cheap at this volume.
- [ ] Lock ordering is a unit test: two opposite transfers in two goroutines must both
      complete.

### The three movements

**Fund a wallet.** A funding source (an operator account, or later the core, or later
a rail) credits a wallet account. Source is a system account; the balance check is
against the source only when the source is a user-owned account.

- [ ] `POST /wallets/{id}/fund` — body: `amount_minor`, `currency`, `source_ref`,
      `reference`.
- [ ] Posts `fund-wallet`: source account −amount, wallet +amount.

**Withdraw from a wallet.** The reverse, into a system account.

- [ ] `POST /wallets/{id}/withdraw` — body: `amount_minor`, `currency`, `destination_ref`.
- [ ] Posts `withdraw-wallet`: wallet −amount, destination +amount.
- [ ] Refuses when derived balance < amount, with `INSUFFICIENT_FUNDS` and the
      available figure in the error detail.

**Transfer between wallets.** Two wallets, one transaction, both legs at once.

- [ ] `POST /transfers/internal` — body: `from_account`, `to_account`, `amount_minor`,
      `currency`, `reference`.
- [ ] Posts `internal-transfer`: from −amount, to +amount. Same currency only.
- [ ] If `from_account == to_account`, reject with `SELF_TRANSFER`.

Because both legs are written in one database transaction, an internal transfer needs
no hold. That is the point: the hold exists for movements that cannot be atomic — the
rail and the multi-service saga — and those arrive in S3.

### The read path

- [ ] `GET /wallets/{id}` — account status, currency, `ledger_balance`, and
      `available_balance`. At this stage the two are equal; S3 makes them diverge.
- [ ] `GET /wallets/{id}/entries?cursor=&limit=` — paginated, newest first, cursor on
      `(created_at, id)`. Entries carry the transaction's `operation_type`,
      `reference` and counterparty summary.

### The internal edge

There is no user authentication yet. This stage exposes the API on the **internal
port only**, behind a static service token from `config` (constant-time comparison,
rotatable). The account identity in the request is trusted from the caller and
recorded as `created_by`.

- [ ] Middleware `requireServiceToken` on the internal router.
- [ ] The public port serves nothing but health until S4.

This is a deliberate, temporary boundary. S4 replaces it rather than extends it: the
service token is deleted, not layered under a JWT.

### Idempotency's absence

These endpoints are **not** idempotent yet. That is acceptable only because nothing
outside the platform can reach them and the only caller is a test suite. Write it in
the README and in the endpoint docs. S3 closes it, and the moment S4 opens the public
edge without S3 being done, this becomes a production incident.

## Data model changes

- [ ] No new tables. The wallet's `reference` becomes `transactions.metadata->>'reference'`
      or a dedicated column — decide now, and prefer a column:

```sql
ALTER TABLE transactions ADD COLUMN reference TEXT NOT NULL DEFAULT '';
CREATE INDEX transactions_reference_idx ON transactions (reference);
```

- [ ] An `accounts_status` guard: a posting to a `frozen` or `closed` account is
      refused. The status check happens under the same lock as the balance check.

## API surface

| Method | Path | Port | Notes |
|---|---|---|---|
| POST | `/wallets/{id}/fund` | internal | `fund-wallet` |
| POST | `/wallets/{id}/withdraw` | internal | `withdraw-wallet`, sufficiency checked |
| POST | `/transfers/internal` | internal | `internal-transfer`, atomic |
| GET | `/wallets/{id}` | internal | Balances |
| GET | `/wallets/{id}/entries` | internal | Cursor pagination |

Full request and response shapes: `reference/api.md`.

## Invariants this stage must leave true

- **Article VIII** — no posting is permitted unless it is covered, and coverage is
  checked under a lock. Tested concurrently, not sequentially.
- **Article II** — the balance in the response is derived at read time. There is still
  no column.
- **Article III** — every endpoint writes a balanced transaction; the refusal paths
  write nothing.
- **Article XI** — `created_by` is the authenticated service identity, recorded.

## Tests you must write

- [ ] **Overdraw test** — the exit criterion, as an automated test at 200 goroutines,
      run in CI (with a smaller N in the fast suite and the full N nightly).
- [ ] **Deadlock test** — A→B and B→A concurrently, repeated 1,000 times; no
      serialization failure escapes the retry loop.
- [ ] **Frozen account test** — a posting to a frozen account is refused and writes
      no entry.
- [ ] **Atomicity test** — force a failure after the first leg is inserted; assert
      neither leg exists.
- [ ] **Self-transfer test** — rejected.
- [ ] **Currency test** — a transfer whose legs disagree in currency is rejected at the
      domain layer and never reaches the store.
- [ ] **Pagination test** — cursor pagination over a ledger with identical timestamps
      (a bulk load) returns every entry exactly once.

## Definition of done

- [ ] The exit-criterion test runs in CI and is not flaky over 50 consecutive runs.
- [ ] `withAccountLocks` is the only path that writes a conditional movement; a grep
      for `FOR UPDATE` finds it and nothing else.
- [ ] No endpoint is reachable from the public port.
- [ ] The README states, in bold, that these endpoints are not idempotent until S3.
- [ ] `reference/api.md` documents every request and response with an example.

## What NOT to build yet

- **Holds.** The internal transfer is atomic, so it does not need one. A hold designed
  here, before the saga exists, will be the wrong hold.
- **Idempotency keys.** They arrive with the saga, because that is when retries become
  real. Do not half-build them now.
- **Fees.** A fee is an entry with a policy behind it, and the policy is S9's. Posting
  a fee leg now hard-codes a rule.
- **User authentication.** The service token is temporary and clearly marked as such.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Negative balance under load | Balance checked outside the lock | Only reproducible under concurrency; the overdraw test catches it |
| Deadlocks at volume | Unsorted lock acquisition | `deadlock detected` in Postgres logs; the sorted-lock test catches it |
| Retry storm on serialization failure | Unbounded retry without backoff | p99 latency climbs with load; retry counter metric |
| Throughput flat at one per account | Row lock serialises all postings | Expected; per-account concurrency is one by design. Sharding is not in scope |

## Handover

Stage 3 inherits three correct, atomic movements that are not idempotent and have no
holds. It adds both, and in doing so it converts the transfer from "one database
transaction" into a saga that survives a process death mid-flight.
