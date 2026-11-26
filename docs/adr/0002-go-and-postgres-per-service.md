# 0002. Go, and one PostgreSQL database per service

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

The platform must move money correctly under concurrency, hold an append-only ledger,
integrate with a core banking system and a national rail, and be run by a small team
without an operations department.

Two choices dominate everything downstream: the language and the persistence
architecture.

## Decision

**Go 1.26** for every service. **PostgreSQL 17**, one database per service with its own
role, on a shared instance.

Every service owns its schema. Cross-service reads happen through an API or a projection
built from events. No service holds credentials for another service's database.

## Alternatives considered

**Node/TypeScript.** Familiar to the team from other projects and faster for
straightforward CRUD. Rejected for the money path: the concurrency model makes
transaction-and-lock scoping easy to get subtly wrong, and the ecosystem's ORMs encourage
mutable row updates, which the ledger forbids. This is a trade-off, not a verdict — the
back office, which is mostly reads and forms, stays Node.

**Java/Kotlin.** Excellent transactional tooling and a mature fintech ecosystem.
Rejected on operational weight: a JVM per service and the associated memory and start-up
cost is a real tax on a small team, and the team's velocity is higher in Go.

**A shared single database.** Simplest for a small team and for cross-cutting queries.
Rejected because it makes Article IX (one owner per fact) unenforceable in practice: a
join is a write waiting to happen, and every service can see every other service's
internal shape. The shared costs are real; the shared *table* is where the boundary
dies.

**A document or KV store.** Rejected: the ledger's correctness rests on multi-row
transactions, `SERIALIZABLE`, and row locks. A store that cannot express those makes the
invariants a property of application code alone.

**Kafka instead of NATS.** Rejected for now: the platform needs ordered-per-aggregate
delivery and at-least-once, which JetStream provides, with far less operational surface.
If the event volume or retention requirements outgrow JetStream, that is a new ADR.

## Consequences

- One language on the server means one CI, one build tool, one profiling story, one
  on-call mental model.
- The database-per-service split costs an extra credential per service and makes
  cross-service queries impossible by construction — which is the point.
- A shared Postgres instance keeps the operational cost low while the logical split does
  the boundary work. Moving a service to its own instance later is a configuration
  change, not a rewrite.
- Team members must be comfortable enough with Go and with SQL to read a generated query
  and know what it does. This is a hiring and code-review requirement, not an incidental
  one.
