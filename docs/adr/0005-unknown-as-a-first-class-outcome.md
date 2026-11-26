# 0005. `Unknown` is a first-class outcome

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

Every call to an external system — the core banking system, a national rail, a partner's
destination — can end in one of three ways: it applied, it refused, or it did not answer.
The third case is the one that causes outages, because the conventional two-state model
(`success`/`error`) maps it to `error`, and an `error` is something a client retries.

Retrying a call that already applied moves money twice. A money platform that cannot
distinguish "did not happen" from "I do not know whether it happened" will eventually
double-credit a customer or double-debit an account, and no amount of downstream
reconciliation makes that acceptable if it happens often enough to notice.

## Decision

Every call to an external money system returns one of exactly three outcomes:
**`Applied`, `Rejected`, or `Unknown`.** `Unknown` is never collapsed into `Rejected`.
An `Unknown` is resolved by **enquiry** against the external reference, never by
retrying the original operation.

The model is carried end to end: through the port interface, the saga state machine, the
`postings` and `transfer_sagas` tables, the HTTP response (`503 outcome_unknown`, not
`5xx` retryable in the usual sense), the event payload (`transfer.unknown`, not
`transfer.failed`), and the documentation.

## Alternatives considered

**Two states with a retry-with-backoff wrapper.** Rejected: retrying is only safe when
the operation is idempotent at the destination, and a national rail's transfer is not
guaranteed to be, especially when the timeout occurs after the rail accepted the request.
The retry is the double-spend.

**A `timeout` flag alongside `success`/`error`.** Rejected as a naming change, not a
modelling change: a flag invites `if timeout { retry }` and the timeout is exactly the
case where retry is wrong.

**Resolve immediately with a synchronous enquiry in the timeout path.** Partially
adopted — the saga does enquiry — but not as the sole mechanism, because the enquiry can
also fail or time out. The platform must be able to hold an unresolved `Unknown` in a
durable state and resolve it later without holding a request open.

## Consequences

- Clients see `unknown` in a transfer's state and know to poll rather than resubmit. The
  API returns `transfer_id` specifically so they can.
- The saga gains a `Resolving` state that an external observer (and the back office)
  takes seriously. It is on the money-health dashboard.
- Some operations take longer to reach a terminal state than a two-state model would
  allow. This is the correct trade: a slow correct answer beats a fast wrong one.
- Code that would have written `return err` now has to decide which of three outcomes it
  holds, and a lint rule refuses a `context.DeadlineExceeded` return that carries a
  non-`Unknown` code (Article VII).
- The model extends outward: the API, the events, the errors document and the operator
  UI all use the same three words. Consistency here is what stops the model being lost at
  the first boundary.
