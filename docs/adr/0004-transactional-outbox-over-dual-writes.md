# 0004. Events leave through a transactional outbox

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

When a transfer completes, several things must learn about it: the notification service,
the webhook service, the back-office projection, and the reconciliation job. Each of
those needs the fact, and each can fail independently of the one that produced it.

The straightforward approach — write the database row, then publish to the broker — has a
fatal window. If the process dies between the two, the money moved and no one was told.
If the broker is down and the code retries the write, the money moved twice.

## Decision

Every event is written to an `outbox` table **in the same database transaction** as the
fact that produced it. A relay process reads unpublished rows and publishes them to NATS,
marking each published only after the publish succeeds.

The transaction commits the fact and the intent to announce it atomically. There is no
moment in which one exists and the other does not.

## Alternatives considered

**Publish directly after commit.** Rejected: crash between commit and publish loses the
event silently. Retry adds duplication without removing loss.

**Two-phase commit between Postgres and the broker.** Rejected: not supported by NATS,
and XA-style protocols are operationally expensive and fragile in ways that are hard to
diagnose at 3am.

**Change data capture (Debezium on the write-ahead log).** Genuinely robust, and a
reasonable alternative. Rejected for now on operational cost: it adds a CDC cluster, a
schema-evolution problem, and a second source of truth about what an event is. The outbox
is a table the team already understands. If event volume or stream variety grows far
beyond what the outbox handles, this becomes a superseding ADR.

**Poll only the domain tables and derive events on read.** Rejected: deriving an event
from current state loses the sequence of changes and produces different answers at
different times for the same past event.

## Consequences

- Publishing is **at-least-once**. A crash after publish and before the mark produces a
  duplicate. Every consumer dedupes on `event_id` in an `inbox` table. This is the cost,
  and it is paid in one well-tested place rather than in every consumer.
- Events are ordered per aggregate because they are published in insertion order and the
  relay preserves it.
- The relay's lag is a first-class metric (`echo_outbox_oldest_unpublished_seconds`). It
  is the health of every downstream consumer in one number.
- The outbox is pruned after publication; it is a delivery mechanism, not a store. An
  event history that must be kept is a projection, not an outbox row.
- Article VI. `internal/platform/events.Publish` is not importable below the relay: the
  compiler enforces that the only way an event reaches the world is through the outbox.
