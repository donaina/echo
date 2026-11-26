# Documentation

This directory is the build. The code will catch up to it.

A payments platform fails in ways that are boring and expensive: a balance that
drifts by a cent, a retry that debits twice, an event that says a transfer succeeded
after the money moved the other way. None of those are caught by reading code under
review. They are caught by having written down, before the first line, what must
never be true — and then testing against the writing.

So the documents here are not commentary on the code. They are the specification
the code is measured against.

---

## Start here

| Read | If you are |
|---|---|
| [`00-constitution.md`](00-constitution.md) | Anyone. Ten minutes. Everything else assumes it. |
| [`phases/00-foundations.md`](phases/00-foundations.md) | Building Stage 0, or joining before Stage 0 exits |
| The phase you are building | An engineer with a stage assignment |
| [`reference/ledger-model.md`](reference/ledger-model.md) | Touching money state |
| [`../CONTRIBUTING.md`](../CONTRIBUTING.md) | Opening your first pull request |

## The map

```
docs/
  00-constitution.md      Twelve articles. The invariants, in prose.
  phases/                 Ten build stages. One document each. This is scope.
    00-foundations.md
    01-ledger-core.md
    02-money-movement.md
    03-idempotency-and-saga.md
    04-identity-and-edge.md
    05-distribution.md
    06-assurance.md
    07-core-posting.md
    08-rail.md
    09-hub-and-pilot.md
  reference/              The parts that outlive any one stage.
    ledger-model.md
    data-model.md
    api.md
    events.md
    errors.md
    testing.md
    observability.md
    security.md
    environments.md
  adr/                    Decisions, with the reasoning that made them obvious.
    README.md             The format, the rules, the index
```

Three kinds of document, three different lifetimes:

- **Constitution** — changed rarely, deliberately, with a reason. If it changes, the
  whole team reads the change.
- **Phases** — written when the stage starts, closed when it exits. The document is
  the stage's contract; it does not grow to cover later work.
- **Reference** — lives across stages and is updated continuously. When the code
  learns something, the reference learns it too.

## Stage numbering versus the programme

The ecosystem document set uses phases **P0–P8** for the whole programme — back
office, core, payments, rail. This repository uses stages **S0–S9** for the payments
build specifically. They are two different clocks. The mapping:

| Stage | Ecosystem phase | Note |
|---|---|---|
| S0 Foundations | P0 | Same work, seen from inside payments |
| S1 Ledger core | P4 | The ledger is the payments half of P4 |
| S2 Money movement | P4 | |
| S3 Idempotency and saga | P4 | |
| S4 Identity and edge | P4 | |
| S5 Distribution | P4 | |
| S6 Assurance | P4 | Assurance runs through every stage; S6 is the formal pass |
| S7 Core posting | P5 | The B seam |
| S8 Rail | P6 | The D seam |
| S9 Hub and pilot | P7, P8 | The C seam, then the pilot |

S1–S6 are one ecosystem phase (P4) split into six because P4 is eight weeks of work
and one document would be a lie about how much is in it. The split is a build
detail, not a change to the programme.

## How a phase document is shaped

Every file in `phases/` has the same sections, in the same order. That is intentional:
after the second one you know where to look.

| Section | Answers |
|---|---|
| Purpose | What this stage is for, in one paragraph |
| Depends on / exit criterion | The contract with the previous and next stage |
| Gate | What must be demonstrated before the stage closes |
| Why this stage exists | Why it is not merged with the one before it |
| What you build | The task list, with checkboxes |
| Data model changes | Migrations this stage adds |
| API surface | Endpoints, gRPC methods, events this stage adds |
| Invariants this stage must leave true | Constitution articles in force, restated concretely |
| Tests you must write | The test tiers required to exit |
| Definition of done | The checklist that closes the stage |
| What NOT to build yet | Scope protection, naming the tempting thing |
| Failure modes | What will go wrong, and the tell |
| Handover | What the next stage inherits |

If a section is empty, the stage is not ready to start.

## How to change a document

- **A typo, a clarity fix** — pull request, no ceremony.
- **A new fact** — pull request, and say in the description which stage or reference
  it came from.
- **A change to the constitution** — a pull request that changes an article, plus an
  ADR explaining why the old article was wrong or insufficient. An article is never
  quietly edited.
- **A change to a closed stage's exit criterion** — it is not a closed stage. Reopen
  it and say so.

## Reading paths by role

**Engineer, first week.** `00-constitution.md` → `reference/ledger-model.md` →
`phases/00-foundations.md` → `../CONTRIBUTING.md`.

**Engineer, ledger or transfer.** `00-constitution.md` → `reference/ledger-model.md` →
`reference/data-model.md` → `phases/01` → `phases/02` → `phases/03`.

**Engineer, edge or identity.** `00-constitution.md` → `reference/api.md` →
`reference/security.md` → `phases/04`.

**Engineer, integrations.** `00-constitution.md` → `reference/events.md` →
`phases/07` → `phases/08` → `phases/09`.

**Reviewer or architect.** `00-constitution.md` → all of `adr/` → the phase currently
open.

**Programme lead.** `../README.md` → `phases/` (purposes and exit criteria only) →
`reference/observability.md`.

## A note on what is deliberately missing

There is no document describing the finished system in detail. It would be a
speculative design for software that does not exist yet, and it would be wrong by
Stage 2. The reference documents describe what exists and what is about to exist. The
phase documents describe what comes next. Nothing describes Stage 9 in code-level
detail, because guessing at Stage 9 on day one is how teams build the wrong Stage 9.
