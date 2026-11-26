# 0001. Record architecture decisions

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

The payments platform is built from scratch, over stages, by a team that will change.
The most expensive mistakes in a money system are not code defects — they are decisions
that were reasonable when made, whose reasoning was never written down, and which are
re-litigated (or worse, silently reversed) a year later by someone who cannot see the
constraint that forced them.

The cost of the decision to reverse is highest exactly where the reasoning is most
likely to be lost: the ledger's shape, the outcome model, the posting contract.

## Decision

Every decision that shapes a boundary, a schema, a protocol, or a whole-system
dependency is recorded as an ADR in `docs/adr/`, numbered, immutable once accepted, and
superseded rather than edited.

## Alternatives considered

**Rely on commit messages and pull requests.** Rejected: they are searchable only by
someone who already knows what happened, and they are attached to code rather than to
decisions.

**Rely on a wiki.** Rejected: it drifts from the code because it is not reviewed with
the code, and closed wikis lose their history.

**Write a design document per stage.** That is what the phase documents are; they
describe what to build. An ADR describes what was decided and what was given up. Both
are needed, and they answer different questions.

## Consequences

- The team pays a small, constant cost of writing a few paragraphs per significant
  decision.
- The phase documents can stop explaining the same trade-off repeatedly and link to the
  ADR.
- New team members can read the ADR index in an hour and know why the system is shaped
  as it is.
- An ADR that is wrong is superseded openly, which makes a reversal a visible decision
  rather than a quiet drift.
