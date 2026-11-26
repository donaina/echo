# The Constitution

Twelve articles. Every one of them is a property of the running system that must
never be false — not at rest, not under load, not during a partial failure, not
while someone is mid-deploy.

They are not guidelines. A pull request that breaks one is rejected even if the
feature it delivers is important, and especially if it is urgent.

Each article states the rule, why it exists, what breaks without it, and the test
that will enforce it.

---

## Article I — Money is `int64` minor units

The only representation of an amount is a signed 64-bit integer counted in the
smallest unit of its currency: kobo, cents, pence. Every field named `amount`,
`balance`, `fee` or `limit` in this system is that type, and every name says so —
`amount_minor`, not `amount`.

**Why.** Floating point cannot represent `0.01`, so a float ledger drifts. Decimal
strings are exact but invite accidental concatenation and comparison errors, and they
make arithmetic allocation-dependent. Integers are exact, ordering is well-defined,
and the database, the wire and the language all agree.

**Without it.** Sums that are off by a cent that cannot be explained, comparisons that
fail for values that print identically, and a reconciliation that never balances.

**Enforced by.** A `go vet`-style lint rule banning `float32`/`float64` in packages
under `internal/ledger` and `internal/wallet`, plus a property test that generates
random amounts and asserts the sum of any balanced entry set is exactly zero.

**The exception.** A display layer may format an amount for a human. That conversion
lives in one named function, takes an explicit currency, and is never used in a
money path.

---

## Article II — A stored balance is never the truth

Balances may be stored. They are a cache. The truth is the entries.

`ledger_balance` is the sum of every posted entry touching an account.
`available_balance` is `ledger_balance` minus the sum of open holds. Any stored
value that disagrees with the entries is a bug in the store, and the entries win.

**Why.** A stored balance is a second copy of a fact, and two copies drift. Drift is
detected only when someone notices, which is too late. A derived balance cannot
disagree with itself.

**Without it.** The Fineract problem, reproduced in Go: a system that recomputes
history slowly and stores a shortcut that is occasionally wrong.

**Enforced by.** A daily (and, once `recon` exists, hourly) integrity job that
recomputes every account's balance from entries and compares with the cache. Any
disagreement opens a break, not a warning.

---

## Article III — Every transaction is zero-sum, per currency

The entries of a transaction sum to zero, in each currency they touch. A transaction
that moves 5,000 kobo out of one account moves exactly 5,000 kobo into others.
Nothing is created, nothing is destroyed, and a fee is an entry, not an adjustment.

**Why.** Double-entry is not a filing convention. It is what makes the ledger
self-checking: if the entries of every transaction sum to zero, the sum over all
accounts is zero always, and any imbalance is unambiguous evidence of a bug.

**Without it.** A ledger where the money supply changes silently, where a fee is
collected into a void, and where reconciliation cannot close.

**Enforced by.** A `CHECK`-style constraint evaluated in the same database statement
that writes the entries — a transaction is written all-or-nothing, and the write
fails if the sum is not zero. Plus a property test over generated transactions.

---

## Article IV — The ledger is append-only

Posted entries are never updated and never deleted. A mistake is corrected by a
reversal: a new, equal-and-opposite transaction that references the original. An
account is closed by a status field, not by removal.

**Why.** An auditable ledger is a history, and history that can be edited is not an
audit trail. A reversal carries who reversed it, when, and why; an update carries
nothing.

**Without it.** A regulator asks what the balance was on a date and no one can say,
because the past was overwritten.

**Enforced by.** No `UPDATE` or `DELETE` grant on `entries` and `transactions` for the
application role. Corrections go through the reversal endpoint, which has its own
audit record.

---

## Article V — Every money write is idempotent

Any request that can move money carries an idempotency key. Two requests with the
same key and the same payload return the same result and move money once. Two
requests with the same key and a *different* payload are a client error, not a
retry.

**Why.** Networks retry. Clients retry. Humans click twice. Without a key, every retry
is a possible second debit. With a key, the second request is a lookup.

**Without it.** The single most common payments incident: a duplicate transfer caused
by a timeout and an impatient retry.

**Enforced by.** The key is required at the transport boundary; a request without one
is rejected before it reaches the domain. A test sends the same request twice and
asserts exactly one transaction exists.

---

## Article VI — Events leave through the transactional outbox

A service writes its state change and the event that announces it in the same
database transaction, into an outbox table. A relay publishes the outbox to NATS
afterward. A service never publishes to the broker and writes to the database as two
independent operations.

**Why.** A dual write has a window where one side succeeds and the other does not,
and no ordering of the two closes it. The outbox makes "the state changed" and "the
event exists" one atomic fact.

**Without it.** Events that announce money movement that did not happen, or money
movement no downstream system ever hears about — and the second is worse.

**Enforced by.** The publish API is not importable below the outbox relay. A test
kills the relay, writes a transaction, restarts the relay, and asserts the event
arrives exactly once.

---

## Article VII — A timeout is `UNKNOWN`, never `FAILED`

When a call that may have moved money does not answer in time, the outcome is
unknown. The system says so, records it, and resolves it out of band — by status
enquiry against the counterparty, or by reconciliation.

**Why.** A timeout means "I do not know". Reporting it as failure invites the client
to retry a debit that already happened, and reporting it as success invites the client
to ship goods against money that never moved. Both are wrong; only "unknown" is true.

**Without it.** Retries that double-debit, and successful transfers that are shown as
failed and then reversed by hand.

**Enforced by.** Every outbound money call returns a three-state result. `UNKNOWN` is
a first-class value with its own persistence, its own metric, and its own resolution
job. A lint rule forbids mapping a context deadline to a failure constant.

---

## Article VIII — Available never exceeds ledger, and holds are explicit

`available = ledger_balance − sum(open holds)`. A debit is permitted only if it is
covered by available balance at the moment of the write. A hold is a row with a
lifecycle: `open → captured | released | expired`.

**Why.** The guarantee a wallet makes is "if this succeeds, the money was there".
Checking against the ledger balance rather than available lets two concurrent
withdrawals each see enough money and together overdraw the account.

**Without it.** A negative balance that the system created, not the customer.

**Enforced by.** The balance check and the debit are one statement under row-level
locking (or a serializable transaction with retry). A concurrency test fires N
simultaneous debits summing to more than the balance and asserts exactly the
affordable number succeed.

---

## Article IX — One owner per fact

Each fact has exactly one service that writes it and every other service that needs
it either reads it over the wire or holds a projection it is allowed to be stale.

- The wallet owns accounts, entries, balances, holds.
- The transfer service owns transfer lifecycle and saga state. It owns no balance.
- The auth service owns credentials and sessions.
- The notification and webhook services own delivery state.
- The recon service owns breaks. It owns no money movement.

No service reads another's tables. A shared schema is not a shortcut; it is the
removal of the boundary the service was built to have.

**Why.** Two writers to one fact are two sources of truth, and two sources of truth
is zero sources of truth. The boundary is what makes each service's tests
meaningful.

**Without it.** A "temporary" join across services that becomes permanent, a
migration that cannot be deployed independently, and a wallet change that quietly
breaks transfer.

**Enforced by.** One database per service with its own credentials; no cross-database
grants. An architecture test asserts the import graph never crosses `internal/<a>` →
`internal/<b>`.

---

## Article X — The core is the book of record, not the query target

The payments platform posts business operations to the core banking system — a
completed wallet funding, a withdrawal, a fee — with a `reference_number`, and
reconciles daily. It does not read balances from the core to serve a customer, and
it does not send the core per-transaction traffic it does not need to book.

**Why.** The core's cost scales with history, and per-transaction traffic against it
is what makes Fineract slow. The payments ledger carries volume; the core books the
day. Separating the two is what keeps the hot path fast.

**Without it.** A payments platform whose latency is set by a core banking system's
recalculation of a two-year-old account.

**Enforced by.** A status-enquiry circuit breaker that fails over to a local decision
rather than blocking on the core, and a reconciliation that makes any divergence
between the two ledgers visible within one day.

---

## Article XI — Every state change is attributable and auditable

Every change to a money object records: what changed, by whom (a service identity or a
named operator via the command API), when (UTC, `timestamptz`), and under what
correlation (`correlation_id`, `causation_id`). A change that cannot answer those
four is not permitted.

**Why.** When a customer asks why their balance is what it is, the answer must be a
list of attributed facts, not a shrug. Audibility is also what makes an incident
investigable: the trace already contains the story.

**Without it.** A dispute that cannot be resolved except by reconstructing intent from
logs that may have rotated away.

**Enforced by.** The audit fields are non-nullable on every money table. A test asserts
that a write without a service identity fails.

---

## Article XII — No gate without evidence

A stage does not close because the work feels done. It closes when its exit criterion
is demonstrated: a test that fails without the work, a migration that applies cleanly,
a document that is accurate, a metric that is visible.

**Why.** "Done" is a feeling. Evidence is a fact. The only way a ten-stage plan
survives contact with a real team is if each stage's completion is checked rather
than asserted.

**Without it.** Stage 3 closing with a flaky test that gets skipped, Stage 7 building
on a Stage 3 that was never actually finished, and a pilot discovering it in
production.

**Enforced by.** The exit criterion section of every phase document, each item having
a named artefact: a passing test, a query result, a dashboard, a signed-off
demonstration. A stage with an unmet criterion is reopened, not waived.

---

## Changing the constitution

An article is changed by a pull request that:

1. Edits the article.
2. Adds an ADR in `adr/` explaining what the old article got wrong.
3. Is reviewed by everyone who owns a service the article touches.

The bar is high because the articles are the last line. If they move with fashion,
they are not a line at all.

Articles are numbered and never renumbered. A retired article keeps its number and
is marked `Retired` with a link to the article that replaced it.
