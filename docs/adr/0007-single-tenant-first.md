# 0007. Single-tenant data model first, tenant column from day one

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

The platform is being built for one institution. There is a strong temptation in both
directions: build a multi-tenant platform from the start (so a second institution can be
added later without a migration), or ignore tenancy entirely (because there is exactly one
institution and a tenant column is a constant that costs an index).

The second is wrong because adding an ownership dimension to a ledger after it holds
money is a migration with real risk. The first is wrong because real multi-tenancy —
per-tenant keys, per-tenant isolation, per-tenant rate limits, per-tenant configuration —
is a large amount of work whose design depends on how the second customer actually
differs from the first.

## Decision

**Single-tenant logic, tenant-aware data.** Every table that holds money or identity
carries a `tenant_id`, every event envelope carries `tenant_id` (see
`reference/events.md`), and the gateway resolves a tenant from the authenticated
principal. The application does not implement per-tenant isolation, per-tenant
configuration, or per-tenant keys in the first release — it assumes one tenant and one
value.

Onboarding a second tenant is therefore a deployment and configuration step (a new
instance, or a partition of the existing one), not a schema migration on a full ledger.

## Alternatives considered

**No `tenant_id` at all.** Rejected: adding it later means backfilling every money table
and every event, and getting the backfill wrong is not recoverable in an append-only
ledger.

**Full multi-tenancy from day one.** Rejected as premature: the isolation model (shared
schema with row-level security, schema-per-tenant, database-per-tenant) depends on the
regulatory and commercial relationship with the second customer, which does not exist
yet. Committing to one now is committing to a guess.

**Row-level security from day one.** A reasonable middle path and a candidate for the
first multi-tenant release — but it needs a tenancy model, and `tenant_id` is the
prerequisite for any of them. This ADR does not forbid it; it defers it.

## Consequences

- Adding a second institution is additive and does not rewrite the ledger's shape.
- The `tenant_id` column is a constant in the first release, so its index is small and
  not load-bearing — indexes can be added when there is a second value.
- Every query that touches a money table carries `tenant_id` in its `WHERE` from the first
  release, which is the habit that makes isolation correct when it is added.
- The event envelope's `tenant_id` is present from day one, so no event consumer has to
  be migrated.
- Someone will ask "why is `t_main` in every row?" and this ADR answers it.
