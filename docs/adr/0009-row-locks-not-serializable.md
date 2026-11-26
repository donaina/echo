# 0009. Row locks, not serializable, for money movements

Status: Accepted
Date: 2026-11-26
Deciders: Platform team

## Context

Stage 2 adds movements that must check a derived balance before debiting: "check,
then debit" is the bug that lets two concurrent withdrawals each see enough money.
Article VIII permits either of two mechanisms — a single statement under row-level
locking, or a serializable transaction with retry.

The stage was written with both: `SELECT ... FOR UPDATE` on every account involved,
in ascending ID order, inside a `SERIALIZABLE` transaction with three retries. The
exit criterion is 200 concurrent withdrawals against an account funded for 100.

That test fails, and not because of a bug in the code. Under `SERIALIZABLE`, a
transaction's snapshot is taken at its first statement. Every withdrawal after the
first blocks on the account row lock with an old snapshot; when the holder commits,
the waiter holds a lock it cannot use, and PostgreSQL aborts it with `40001`
("could not serialize access due to read/write dependencies"). It retries, takes its
place at the back of the queue, and is very likely to be aborted again. With N
waiters the retries are O(N) per transaction and O(N²) in total, which is a retry
storm, not a bounded retry.

## Decision

A movement that depends on a balance runs in a **`READ COMMITTED`** transaction and
takes `SELECT ... FOR UPDATE` on every account it touches, in ascending account-ID
order, before it reads the balance. The row lock is the guarantee.

Every writer of entries follows the same protocol: the three movements, and
`Reverse`, which takes the account locks of the transaction it reverses. A reader
that needs a consistent balance is a reader inside one of those transactions; the
ledger-integrity job is read-only and does not need the lock protocol.

## Alternatives considered

**Serializable with more retries.** Rejected on arithmetic, not taste: under
contention each retry re-queues behind the same waiters, so the retry count grows
with the number of concurrent callers. A fixed bound either fails the exit criterion
or hides the failure behind a long test.

**Serializable with application-level backoff.** Rejected for the same reason at
lower load, and it makes latency a function of queue position rather than of work.

**Advisory locks keyed on account ID.** A correct alternative. Rejected for now
because it is a second locking mechanism alongside the row lock the write already
needs, and two mechanisms are two things to get wrong.

**An optimistic version column on accounts.** Rejected: every entry writer would
need to bump it, which is the same invariant with an extra column to miss, and
conflicts would still need a retry loop.

## Consequences

- Coverage is guaranteed by the lock, and the balance read after the lock sees every
  committed predecessor. The 200-way overdraw test passes deterministically.
- Any future writer of entries must take the account locks. That is a rule the
  boundary test cannot enforce, so it lives in `ledger-model.md` and in this ADR.
  `PostTransaction` remains the low-level primitive used by tests and tooling; the
  service's money paths do not call it directly.
- Throughput per account is one posting at a time, by design. The stage document
  already says so; sharding is not in scope.
- A deadlock is still possible only if someone locks out of order, which is why the
  helper sorts and compacts the IDs. The three-attempt retry remains as a safety net.
