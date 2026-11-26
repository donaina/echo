# Architecture decisions

An ADR records a decision that was made, why, and what it costs. It is not a design
document — the phase documents are those. An ADR is short, permanent, and written by the
person who made the decision while the reasoning is still fresh.

## Why bother

Six months from now someone will ask "why is there no balance column?" The honest answer
is a paragraph, not a reconstruction from the code. The ADR is that paragraph, and it is
reviewable before the decision is expensive to undo.

## Format

```
# NNNN. Title in the imperative

Status: Proposed | Accepted | Superseded by NNNN | Deprecated
Date: YYYY-MM-DD
Deciders: names or roles

## Context
What forced a decision. The constraints, in plain language.

## Decision
What we will do. One paragraph, active voice.

## Alternatives considered
What we rejected, and why. This is the most useful section later.

## Consequences
What this makes easy, what it makes hard, and what we will have to live with.
```

## Rules

- **Numbered sequentially, never reused.** A deleted decision keeps its number and its
  file with `Status: Deprecated`.
- **Immutable once accepted.** A changed decision is a new ADR that supersedes the old
  one; the old one is edited only to add `Superseded by NNNN`.
- **One decision per ADR.** An ADR with two decisions is two ADRs.
- **Proposed → Accepted** at merge. An ADR that never leaves Proposed is a design sketch
  and belongs in the pull request, not here.
- **Status is honest.** Accepted means it is what the code does today.

## Index

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-go-and-postgres-per-service.md) | Go, and one PostgreSQL database per service | Accepted |
| [0003](0003-integer-minor-units.md) | Money is `int64` minor units | Accepted |
| [0004](0004-transactional-outbox-over-dual-writes.md) | Events leave through a transactional outbox | Accepted |
| [0005](0005-unknown-as-a-first-class-outcome.md) | `Unknown` is a first-class outcome | Accepted |
| [0006](0006-posting-adapter-sends-business-operations.md) | The posting adapter sends business operations, not GL legs | Accepted |
| [0007](0007-single-tenant-first.md) | Single-tenant data model first, tenant column from day one | Accepted |
| [0008](0008-verified-checkpoints-not-stored-balances.md) | Balances are derived; checkpoints are a verified cache | Accepted |
| [0009](0009-row-locks-not-serializable.md) | Row locks, not serializable, for money movements | Accepted |

## When to write one

Write an ADR when the decision:

- Is expensive to reverse (a schema shape, a protocol, a database).
- Has more than one reasonable answer and a reviewer will ask "why not X?".
- Changes a boundary between services.
- Adds or removes a dependency the whole project carries.
- Amends the constitution.

Do not write one for a decision a senior reviewer would not question. A directory of
obvious ADRs hides the three that matter.
