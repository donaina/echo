# S5 — Distribution

**Weeks 11–13 · Gate G3 · Owner: service engineer**

## Purpose

Get events out of the database and into the world: a reliable relay from the outbox
to NATS, consumers that cannot act twice on one event, notification delivery to users,
and signed webhook delivery to partners.

This is where the platform starts talking. Up to now every fact stayed inside the
service that created it.

## Depends on

S3 (the outbox table and its events) and S4 (an authenticated user to notify, a
partner identity to subscribe as).

## Exit criterion

A completed transfer produces exactly one NATS message, exactly one notification
attempt to the sender, and exactly one signed webhook delivery to a subscribed
partner. Killing the relay mid-flight and restarting it delivers the event exactly
once to every consumer. A webhook endpoint that returns 500 three times receives
retries with backoff and then a dead letter, visible in an operator query.

## Gate

**G3 — synthetic release readiness.** Delivery is observable, replayable and bounded.

## Why this stage exists

Because "and then it publishes an event" hides four hard problems: the relay crashing
between reading and marking, the consumer receiving the same message twice, the
partner's endpoint being down, and the event arriving before the partner has finished
processing the previous one. Each has a standard answer, and none of them is optional
in a system that moves money.

## What you build

### The relay

- [ ] A publisher loop per service with an outbox: select unpublished rows
      `ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT n`, publish, then mark
      `published_at` in the same transaction that held the lock.
- [ ] **Correctness under crash:** publishing happens before marking; if the process
      dies after publish and before commit, the row is still unpublished and is
      republished. That produces a duplicate — which the consumer resolves. This is
      at-least-once, and it is the only honest delivery guarantee.
- [ ] `LISTEN/NOTIFY` wakes the publisher on insert; a poll every second is the
      fallback. No busy loop.
- [ ] Backpressure: if NATS is unavailable, the outbox grows and the metric
      `outbox_oldest_unpublished_seconds` alerts. The service keeps serving.
- [ ] Ordered publishing per aggregate: rows are grouped by `aggregate_id` and
      published in `created_at` order; the relay does not parallelise within one
      aggregate.

### The event envelope

Every event, on every subject, has the same envelope. Consumers depend on it.

```json
{
  "event_id":       "01J...",          // ULID, unique per event
  "type":           "transfer.completed",
  "occurred_at":    "2026-11-24T09:31:07.412Z",
  "tenant_id":      "t_...",
  "correlation_id": "10f3...",
  "causation_id":   "01J...",          // the event or request that caused this one
  "partition_key":  "acct_...",         // ordering scope; usually the money-bearing account
  "schema_ref":     "payments.transfer.completed.v1",
  "data": { ... }
}
```

- [ ] The envelope is produced by one function in `internal/platform/events`; no
      service hand-builds one.
- [ ] `schema_ref` carries the version. A breaking change is `...v2`, published beside
      `v1` during a dual-publish window.

### NATS JetStream

- [ ] Streams: `PAYMENTS_TRANSFER`, `PAYMENTS_WALLET`, `PAYMENTS_IDENTITY`, one per
      domain, subjects `payments.<domain>.<event>`.
- [ ] Retention: limits-based (age and size), not interest-based, so a slow consumer
      cannot stall the stream. Limits documented in `deploy/`.
- [ ] Consumers are durable, named per service, `AckExplicit`, with `MaxDeliver` set
      and a dead-letter subject for messages that exceed it.
- [ ] `Nak` with a delay on transient failures; `Term` on poison messages after the
      policy limit.

### Consumer idempotency

- [ ] Every consumer writes into an `inbox` table:
      `(consumer_name, event_id) PRIMARY KEY` — and processes the event in the same
      transaction that inserts into `inbox`. A duplicate insert means the event was
      already processed; ack and move on.
- [ ] A test publishes the same event three times and asserts one effect.

### The notification service

- [ ] `notifications` table: `id`, `user_id`, `channel` (`email`/`sms`), `template`,
      `payload` (no secrets), `status`, `attempts`, `next_attempt_at`, `provider_ref`.
- [ ] A provider interface with two implementations: a real SMTP/SMS adapter and a
      logging adapter for development. Providers are selected by config.
- [ ] Templates are files in the repository with typed parameters; a missing parameter
      is a startup failure, not a runtime failure.
- [ ] Retry with exponential backoff and jitter; dead-letter after N attempts;
      `notifications_dead` is an alert.
- [ ] User preferences: opt-out per channel is honoured for non-transactional
      messages. Transactional messages (a transfer receipt) are not opt-out-able.
- [ ] It consumes `transfer.completed`, `transfer.failed`, `wallet.credited`, and
      later `dispute.*`.

### The webhook service

- [ ] `subscriptions`: partner id, url, event types, secret (encrypted at rest),
      status, created_at.
- [ ] `deliveries`: `id`, `subscription_id`, `event_id`, `payload`, `attempts`,
      `last_status`, `next_attempt_at`, `delivered_at`.
- [ ] Signing: `Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`.
      Receivers must reject timestamps older than five minutes — documented in
      `reference/events.md` with a verification example.
- [ ] Retry schedule: 10s, 30s, 2m, 10m, 1h, 6h, 24h — then dead letter. Jittered.
- [ ] Endpoint health: a subscription auto-pauses after sustained failure, and a
      partner can see it in the API.
- [ ] Replay: `POST /v1/webhooks/subscriptions/{id}/replay` with an event id range,
      operator-only, audited.
- [ ] SSRF protection: URLs are resolved to public addresses only; loopback, link-local
      and RFC 1918 targets are refused at subscription time and at delivery time.

## Data model changes

Per service: `inbox` (consumers), `notifications` (notification), `subscriptions` and
`deliveries` (webhook). The `outbox` gains the `LISTEN` trigger.

## API surface

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/webhooks/subscriptions` | Partner creates a subscription |
| GET | `/v1/webhooks/subscriptions` | List own subscriptions |
| DELETE | `/v1/webhooks/subscriptions/{id}` | Disable |
| POST | `/v1/webhooks/subscriptions/{id}/replay` | Operator only, audited |
| GET | `/v1/notifications` | User's own notification history |

## Invariants this stage must leave true

- **Article VI** — no service publishes to NATS outside the relay. A test greps for
      direct publishes.
- **Article VII** — an event is never emitted for an outcome that is unknown; a saga
      in `Resolving` emits nothing until it resolves.
- **Article XI** — every event carries `correlation_id` and `causation_id`, so a chain
      of effects traces back to one request.

## Tests you must write

- [ ] **Relay crash** — kill between publish and mark; the event arrives; the consumer
      sees it twice; one effect.
- [ ] **Duplicate delivery** — publish the same `event_id` three times; one inbox row,
      one effect.
- [ ] **Poison message** — an event the consumer cannot parse is `Term`'d to the DLQ
      after the limit, not retried forever.
- [ ] **Webhook retry** — a 500 endpoint receives the retry schedule and then a dead
      letter.
- [ ] **Webhook signature verification** — the documented verification example passes;
      a tampered body fails.
- [ ] **SSRF** — a subscription to `http://127.0.0.1` and to `http://169.254.169.254`
      is refused.
- [ ] **Notification template** — a missing template parameter fails at startup.
- [ ] **Ordering** — two events for the same `partition_key` are consumed in order.

## Definition of done

- [ ] `reference/events.md` lists every event type, its envelope and its `data` shape,
      with a version policy.
- [ ] `outbox_oldest_unpublished_seconds` and `webhook_delivery_failure_rate` are on
      a dashboard.
- [ ] The event catalogue is generated from code or checked against it in CI.
- [ ] No service publishes to NATS except through the relay.

## What NOT to build yet

- **The event feed for TOVARYN.** It reads the same events through a narrow API, but
  the projection and the feed endpoint are S9's — they need the command API beside them.
- **Partner OAuth.** Partner identities are operator-created at this stage; self-service
  is S9.
- **Multi-tenant streams.** Single tenant first; the envelope carries `tenant_id` so
  the change is additive.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Events publish twice permanently | Marking before publishing | Duplicate rate on consumers stays elevated forever |
| Outbox grows unboundedly | Relay dead or NATS unreachable | `outbox_oldest_unpublished_seconds` climbing |
| A partner receives events out of order | Parallel publishing per aggregate | Their processing sees `completed` before `reserved` |
| Webhooks delivered to internal services | SSRF check missing at delivery time | A redirect to `169.254.169.254` succeeds |

## Handover

Stage 6 is the assurance pass: load, race, fuzz, chaos and the observability that
proves all of this works at the volumes the pilot implies. Everything from S1–S5 is
now present; S6 is where it is measured.
