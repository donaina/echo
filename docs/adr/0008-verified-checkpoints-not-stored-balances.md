# 0008. Balances are derived; checkpoints are a verified cache

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

Deriving a balance by summing a customer's entries is exact and unfalsifiable, and it
gets slower as history grows. This is not a theoretical concern. It is the observed
failure mode of core banking systems that compute balances from full history: an account
with a long transaction history derives slowly enough that a transfer against it times
out, and the fix that gets reached for is a cache in front of the balance read.

A naive cache is worse than the problem. It is a second source of truth that can be
wrong in ways nobody detects, it is invalidated incorrectly under concurrency, and it
turns a correctness bug into a latency bug that hides the correctness bug.

The alternative answer — a stored `balance` column updated on every write — is the same
mistake with a different name: it is a materialised truth that must be kept exactly right
by every code path, forever, and the one code path that misses it is found by a customer.

## Decision

**No stored balance is ever the source of truth.** `available = ledger − open holds`, and
`ledger` is the sum of an account's entries.

Performance is addressed with **`balance_checkpoints`**: a row
`(account_id, as_of_entry_id, balance_minor)` that records "the sum of entries up to and
including `as_of_entry_id`". A balance read becomes:

```
balance = checkpoint.balance_minor
        + SUM(entries.delta_minor WHERE account = ? AND id > checkpoint.as_of_entry_id)
```

A checkpoint is **not a source of truth**. It is a cache with two rules:

1. **Created under the account's row lock**, in the same transaction as the entries it
   summarises up to, so it can never describe a state that never existed.
2. **Verified or deleted.** An integrity job recomputes the balance from full history
   (daily for all accounts, hourly for a sample) and, on any mismatch, **deletes the
   checkpoint and opens a break**. A cache that can be wrong is a cache that must be
   provably right or absent.

The same rule governs any future cache — Redis, a projection, a view. If a value can be
wrong and nobody notices, it is a cache, and it is not allowed to be the answer.

## Alternatives considered

**A stored `balance` column, updated in every write path.** The common design in
transactional systems. Rejected for the ledger: it is a derived value promoted to truth,
and every one of the dozens of write paths must update it correctly, including reversal,
adjustment, hold capture, and any future operation. One missed path is a permanent silent
divergence.

**A Redis balance cache.** Rejected as a primary answer, for the same reason: it is a
second truth, with weaker durability. Redis is used for rate limits, a session denylist,
and short-lived projections — things whose loss is recoverable and whose staleness is
bounded.

**Derive on every read, with no cache at all.** This is the honest starting point and it
is what S1–S2 do. It is correct, and it is the fallback for any account whose checkpoint
is deleted. It is not viable at five million entries behind a single account, which is
why checkpoints exist — at S6, when the numbers justify them, not before.

**Partitioning the entries table to keep the sum fast.** A legitimate later tool for a
different problem (storage and vacuum), but it does not make a fresh sum over a large
history fast, which is the actual concern.

## Consequences

- Every balance has a derivation that can be re-run and compared. Divergence is
  detectable, which means it is fixable.
- A slow read becomes a `< 50 ms` read once a checkpoint covers most of the history, and
  the measurement (`echo_balance_derivation_duration_seconds`, split by full and
  checkpoint) is on a dashboard.
- The integrity job costs real compute. It is the price of a cache that cannot lie
  quietly, and it is cheaper than a balance that is wrong.
- Checkpoints can be deleted wholesale and rebuilt from entries; nothing is lost. This is
  what "verified cache" means: an object you can throw away and get back.
- Article II. The ledger package has no `UPDATE accounts SET balance` statement and no
  column to hold one; a review that adds one is a review of this ADR.
