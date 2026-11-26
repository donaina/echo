# Testing

The tests are not a safety net around the system. In a ledger, the tests are how you
know what the system does, because reading the code cannot tell you whether two
concurrent withdrawals overdraw an account.

---

## The five tiers

| Tier | Command | Runs against | Budget | What it must prove |
|---|---|---|---|---|
| Unit | `make test` | nothing | < 2 s | Domain rules and pure functions |
| Property | `make test` | nothing | < 10 s | Invariants over generated input |
| Integration | `make test-integration` | real Postgres, real NATS | < 3 min | Schema, transactions, relay, consumers |
| Contract | `make test-contract` | recorded schemas | < 30 s | Producers and consumers agree |
| End-to-end | `make test-e2e` | full compose stack | < 10 min | A user journey with real money movement |

A test belongs to exactly one tier. A test that needs a database and also asserts a
domain rule is an integration test; the domain rule should have a unit test too.

## What each tier is for

**Unit.** Fast, and therefore run constantly. All balance arithmetic, the saga's
transition function, the idempotency fingerprint, the rail code mapping. If a unit
test needs a database, it is not a unit test.

**Property.** The tier that catches what examples miss. Builders: `testing/quick` or
`pgregory.net/rapid`. Rules: generators produce valid inputs by construction, shrinks
are checked, and every failure names the seed.

**Integration.** Where the database's constraints are tested as constraints. If the
zero-sum check lives only in Go, the integration test proves the database would not
save you. If it lives in SQL too, the test proves the SQL is right.

**Contract.** The producer's schema is checked against the consumer's expectations. A
producer change that would break a consumer fails here rather than in production.
Every event type and every endpoint has a contract.

**End-to-end.** One test per user journey, not one per endpoint. Register, fund,
transfer, receive webhook. A journey that passes is worth more than fifty endpoint
tests that each pass in isolation.

## The invariant sweep

The constitution's enforcement column names a test per article. Those tests are
collected in one package, `internal/assurance`, so that a change to an article has
exactly one place to change its proof.

| Article | Test | Tier |
|---|---|---|
| I — integer minor units | lint + `TestZeroSumOverGeneratedSets` | unit |
| II — derived balance | `TestBalanceEqualsSumOfEntries`, `TestCheckpointEquivalence` | property |
| III — zero-sum | `TestUnbalancedTransactionRejected` | integration |
| IV — append-only | `TestEntriesAreImmutable` (permission test) | integration |
| V — idempotent writes | `TestIdempotentReplay`, `TestConcurrentSameKey` | integration |
| VI — outbox | `TestRelayCrashNoEventLoss` | integration |
| VII — unknown | `TestTimeoutYieldsUnknown` | integration |
| VIII — available ≤ ledger | `TestConcurrentOverdraw` | integration |
| IX — one owner | `TestImportGraphBoundaries` | static |
| X — core not a query target | `TestNoCoreReadOnHotPath` (import + call graph) | static |
| XI — attributable | `TestWriteRequiresActor` | integration |
| XII — gate evidence | the stage exit criteria themselves | process |

If an article has no test listed, the table is wrong and the build fails — the table is
generated from the test registry, not maintained by hand.

## Property tests

The four that matter most:

**Zero-sum.** Generate a random set of legs; `BuildTransfer` accepts exactly when the
signed sum is zero and currencies match. Generate millions of cases; the shrinking
input is kept in `testdata/fuzz`.

**Checkpoint equivalence.** Build a ledger of random entries, insert checkpoints at
random boundaries, and assert derived balance equals full recomputation.

**Saga legality.** Generate random sequences of events and failures; assert every
resulting state is reachable and every terminal state has no open hold belonging to it.

**Idempotency canonicalisation.** Generate JSON objects; assert that any permutation of
keys and insignificant whitespace produces one fingerprint.

## Fuzzing

| Target | Why |
|---|---|
| amount and currency parsing | The edge between strings and `int64` is where overflow lives |
| idempotency fingerprint | Canonicalisation bugs defeat idempotency silently |
| JWT parsing | Untrusted input on the public edge |
| webhook signature | Constant-time comparison, malformed headers |
| rail response parsing | Unmapped codes must become `Unknown`, never panic |
| saga transition function | Illegal transitions must be rejected, never applied |

Nightly: 10 minutes per target. A crash becomes a regression test in the suite before it
becomes a fix.

## Concurrency tests

These are the ones most likely to be skipped and most likely to matter.

- **Overdraw:** N goroutines, balance covering M < N. Exactly M succeed.
- **Reverse transfers:** A→B and B→A simultaneously, 1,000 iterations. No deadlock.
- **Concurrent holds:** N reserves against M < N. Exactly M open holds.
- **Same idempotency key concurrently:** one effect, N responses, no `500`.
- **Saga under kill:** kill between every pair of states; resume; one effect.

Run with `-race`, always. A concurrency test without the race detector tests the
scheduler, not the code.

## Load

`deploy/load/` holds the profiles. Each run asserts both performance and invariants.

```
make load-steady     # 200 TPS, 30 min, internal transfers
make load-spike      # 20 → 400 TPS in 30 s
make load-read       # 1,000 TPS balance reads
```

A load run that meets its latency target and leaves an unbalanced ledger has failed.
The invariant check is part of the run, not an optional extra.

## Chaos

Scripted, with pass conditions written before running:

| Scenario | Pass condition |
|---|---|
| Kill the saga worker | No lost transfer; every saga terminal within the window |
| Kill the relay | No lost event; lag recovers within 60 s |
| Drop NATS for 60 s | Money keeps moving; lag recovers |
| Pause Postgres 30 s | Requests fail fast with 503; no hung goroutines after recovery |
| Inject 2 s into the destination | `Unknown` recorded; resolved by enquiry |
| Clock skew ±5 min | Tokens and webhooks behave per documented tolerance |

## What is deliberately not tested

- **Third-party libraries.** Testing `pgx` or `chi` is testing someone else's contract.
- **Configuration that cannot be wrong.** A value is validated at startup or not at all.
- **The database's own guarantees.** Postgres does not need a test proving it rolls
  back.
- **Coverage percentage.** A test that exists to raise a number is a maintenance cost.

## Flaky tests

A flaky test is a failed test. It is fixed or deleted in the same week it appears; it
is never marked `skip` and left.

The rules:

1. A flaky test blocks the release, so there is pressure to fix it.
2. Time-dependent tests use a fake clock.
3. Network-dependent tests use a local server, not a remote one.
4. Order-dependent tests are broken, not serialised.
5. Retries are not used to hide flakiness. A retried test is a test that lies about its
   own reliability.

## Test data

- Tests build state through the API or the ledger's own constructors. Direct `INSERT`
  into `entries` is forbidden — it bypasses the invariant the test is meant to check.
- Factories generate valid objects with deterministic seeds.
- No test depends on another test's data. Parallel-safe by default.

## CI

On every pull request: unit, property, integration, contract, race.
Nightly: end-to-end, load (steady), chaos, fuzzing, the full invariant sweep at a
larger N.

A pull request that makes CI slower by more than 10% is a conversation, not an
automatic merge.
