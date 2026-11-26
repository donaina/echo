# Events

Events are how the platform tells the rest of the world what happened. They are written
by the service that owns the fact, in the same transaction as the fact, to an outbox,
and published afterwards.

This document is the contract: the envelope, the catalogue, and the rules a consumer
may rely on.

---

## Delivery guarantees

**At-least-once.** Every event that is committed to the outbox is published at least
once. A crash between publishing and marking the row published produces a duplicate.

**Not exactly-once.** No broker offers it, and a system that claims it has moved the
problem into a consumer that silently drops events. Consumers must be idempotent.

**Ordered per aggregate.** Events for one `aggregate_id` are published in
`occurred_at` order. Events across aggregates have no order guarantee.

**No global order.** Do not build a consumer that depends on seeing `wallet.credited`
after every `transfer.completed`. Build one that keys on `correlation_id` when it needs
the chain.

## The envelope

```json
{
  "event_id":       "01J9C4Z6Q8V2N5R7T1X3B9M0K7",
  "type":           "transfer.completed",
  "occurred_at":    "2026-11-24T09:31:07.412Z",
  "tenant_id":      "t_main",
  "correlation_id": "10f3a2b8-6f7e-4c2a-9b1d-3e5f7a9c1b2d",
  "causation_id":   "01J9C4Y...",
  "partition_key":  "01J8Z...",
  "schema_ref":     "payments.transfer.completed.v1",
  "data": { }
}
```

| Field | Rule |
|---|---|
| `event_id` | ULID, globally unique, stable across republication. **This is the dedupe key.** |
| `type` | `domain.entity.action`, lowercase, dot-separated |
| `occurred_at` | When the fact became true, UTC |
| `tenant_id` | Present from day one so multi-tenancy is additive |
| `correlation_id` | The original request's id; identical across the whole chain |
| `causation_id` | The immediate parent: an event id, or a request id for the origin |
| `partition_key` | Ordering scope; normally the money-bearing account |
| `schema_ref` | `payments.<type>.v<n>`; versioned |
| `data` | The payload; no envelope fields repeated, no secrets, no PII |

Amounts in `data` are always `amount_minor` plus `currency`. A payload with an amount
and no currency fails the schema check in CI.

## Subjects and streams

| Stream | Subjects | Events |
|---|---|---|
| `PAYMENTS_TRANSFER` | `payments.transfer.>` | Transfer and saga lifecycle |
| `PAYMENTS_WALLET` | `payments.wallet.>` | Ledger-visible money facts |
| `PAYMENTS_IDENTITY` | `payments.identity.>` | User lifecycle |
| `PAYMENTS_OPS` | `payments.ops.>` | Breaks, settlements, postings |

Streams use limits-based retention (age and size), documented in `deploy/`. A stream
never blocks a publisher because a consumer is slow; a slow consumer falls behind and
alerts.

## Catalogue

### Transfers

| Type | When | Key payload fields |
|---|---|---|
| `transfer.received` | The saga exists and has accepted the request | `transfer_id`, `source_account`, `destination`, `amount_minor`, `currency` |
| `transfer.reserved` | Funds held at source | + `hold_id` |
| `transfer.completed` | Money moved, both sides acknowledged | + `transaction_id`, `remote_ref` |
| `transfer.failed` | No money moved; hold released or debit reversed | + `failure_code`, `failure_reason` |
| `transfer.unknown` | Outcome not yet known; resolving | + `remote_ref`, `resolution_started_at` |

Naming is deliberate: `transfer.completed` means money moved. `transfer.unknown` is not
a failure and must not be treated as one.

### Wallet

| Type | When | Key payload fields |
|---|---|---|
| `wallet.credited` | An entry with positive delta posted to a wallet | `account_id`, `transaction_id`, `amount_minor`, `currency`, `operation_type` |
| `wallet.debited` | An entry with negative delta posted to a wallet | same |
| `wallet.hold.created` | A hold opened | `hold_id`, `account_id`, `amount_minor` |
| `wallet.hold.released` | A hold released or expired | `hold_id`, `reason` |
| `wallet.frozen` | Account status changed to frozen | `account_id`, `reason` |

`wallet.credited` and `wallet.debited` are emitted per account leg. One internal
transfer produces one of each — the payer sees `debited`, the payee sees `credited`.
They share a `correlation_id`, which is how a consumer joins them.

### Identity

| Type | When |
|---|---|
| `identity.user.created` | Registration succeeded |
| `identity.user.locked` | Too many failed attempts, or operator action |
| `identity.session.revoked` | Logout, refresh reuse detection, or operator action |

Identity events carry a user id and never an email, phone or credential.

### Operations

| Type | When |
|---|---|
| `ops.posting.posted` | The core acknowledged a posting |
| `ops.posting.rejected` | The core rejected a posting |
| `ops.posting.unknown` | The core did not answer |
| `ops.break.opened` | Reconciliation opened a break |
| `ops.break.closed` | A break was resolved with evidence |
| `ops.settlement.closed` | A settlement cycle closed with a net position |

These are the back office's primary signal. They are consumed by the projection and by
the break notification path.

## Versioning

- Additive changes to `data` do not bump the version.
- Removing or redefining a field bumps `v<n>` and publishes both during a window of at
  least 90 days.
- A consumer that meets an unknown `schema_ref` version must not guess. It logs, drops,
  and alerts — silently guessing at a money event is worse than missing it.
- `schema_ref` is checked against registered schemas in CI. A producer that emits an
  unregistered shape fails the build.

## Consuming

The rules, in full:

1. **Dedupe on `event_id`.** Insert into an `inbox` table keyed by
   `(consumer_name, event_id)` in the same transaction as the effect. A conflict means
   already processed.
2. **Ack after the effect.** `Ack` only after the transaction commits. `Nak` for
   transient failures with a delay; `Term` for poison messages after the configured
   delivery limit.
3. **Do not assume order across aggregates.** Order within a `partition_key` is
   guaranteed; nothing else is.
4. **Do not assume the payload has everything.** If a consumer needs current state, it
   asks the owner. An event is a statement about the past.
5. **Treat an unknown version as an error, not as data.**
6. **Never write to another service's database from a consumer.** Consume into a
   projection you own.

## Webhooks

Partners receive a filtered subset of these events, delivered over HTTP.

### Signature

```
Webhook-Signature: t=1732444267,v1=5f2c...
```

`v1` is `hex(HMAC-SHA256(secret, t + "." + raw_body))`, where `t` is the Unix seconds
in the header.

Verification, in any language:

```
signed_payload = timestamp + "." + raw_request_body
expected       = HMAC-SHA256(secret, signed_payload)
accept if hex(expected) == v1  AND  |now - timestamp| < 300 seconds
```

Compare with a constant-time comparison. Reject if the timestamp is more than five
minutes old — otherwise a captured delivery can be replayed forever.

### Delivery

- `POST` to the subscription URL with `Content-Type: application/json` and the event
  envelope as the body.
- `Webhook-Id` header carries `event_id`; a partner dedupes on it exactly as an
  internal consumer does.
- Retry schedule: 10 s, 30 s, 2 m, 10 m, 1 h, 6 h, 24 h. Then dead letter, visible to
  operators and to the partner in their subscription view.
- Any 2xx is success. `410 Gone` disables the subscription immediately — the partner
  has said the endpoint is gone.
- Redirects are not followed. This is both a correctness and an SSRF rule.

### What is never sent

- No PII beyond the partner's own customer reference.
- No tokens, secrets or internal ids that are not already partner-facing.
- No event the subscription did not ask for, even if it is interesting.

## Adding an event

1. Define the type and payload in `api/schemas/events/`.
2. Add it to the catalogue above and to the registry.
3. Emit it through the outbox in the owning service's transaction.
4. Write a producer test that the envelope is complete and the schema matches.
5. If it is partner-visible, add it to the webhook allowlist and document it for
   partners before enabling.

An event is a public promise. Adding one is cheap; changing one is not.
