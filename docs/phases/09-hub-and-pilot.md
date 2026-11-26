# S9 — Hub and pilot

**Weeks 22–28 · Gate G4 → G5 · Owner: whole team**

## Purpose

Make the platform operable by people who did not build it, connect it to the back
office through the narrow interfaces those people need, harden everything that a
pilot will expose, and run the pilot.

This is the last stage, and it is the one where the quality of the previous eight is
tested by strangers.

## Depends on

S0–S8. Every seam built; every invariant enforced; every failure mode instrumented.

## Exit criterion

A pilot cohort of real users on the real rail for four weeks, with:

- zero unexplained ledger divergence, and every break closed with evidence;
- all SLOs met, or the misses explained and accepted;
- the back office running the daily cycle — approvals, breaks, settlements — without
  engineering intervention;
- two incidents handled by the on-call rotation using only the runbooks.

## Gate

**G4 — institutional pilot**, then **G5 — production expansion** on a documented
go/no-go.

## Why this stage exists

Because a system that only its authors can operate is not finished, and because the
first real users will find the things the test suite could not imagine. The pilot is
not a launch; it is the last stage of assurance, conducted with real money and a small
blast radius.

## What you build

### The event feed (seam C, outbound)

The back office needs to see what happened. It does not need to be in the path.

- [ ] A read-only projection service: consumers subscribe to the platform's events and
      maintain a denormalised view of transfers, wallets, holds, disputes and breaks,
      shaped for the back office's screens.
- [ ] `GET /v1/feed/events?cursor=&types=&since=` — an ordered, cursor-paginated event
      stream. Cursor is opaque and stable across reconnects.
- [ ] At-least-once delivery, deduped by `event_id` at the consumer. Documented, with
      an example.
- [ ] The feed never carries an amount without its currency, and never carries a
      credential or a raw account number.
- [ ] The projection lags; it is allowed to. Every feed item carries `occurred_at` and
      the consumer is told the projection's `as_of` watermark.

The feed is broad on purpose. Reads are cheap, safe, and the back office needs
visibility.

### The command API (seam C, inbound)

The back office has four ways to *change* the platform. Four, not forty. Every
addition is an ADR and a review of who is allowed to call it.

| Command | Effect | Why it exists |
|---|---|---|
| `transfer.placeHoldForReview` | Places a review hold on a transfer's funds without moving them | A compliance or fraud review must stop money without destroying it |
| `transfer.releaseReviewHold` | Releases a review hold | The review ended with no finding |
| `dispute.register` | Creates a dispute case linked to one or more transactions | Disputes must be traceable to facts |
| `statement.request` | Generates a statement for an account over a date range | Customers and regulators ask for statements |

- [ ] Each command is a named operation with its own audit record, actor, reason and
      `correlation_id`.
- [ ] Commands are **idempotent** by `command_id`, required.
- [ ] Commands are authorised by a service identity with a scope per command, issued
      by the operator's identity system, not by the platform's user tokens.
- [ ] `placeHoldForReview` creates a hold of type `review` that is excluded from expiry
      and from `available` calculation until released by command. It cannot be
      captured by any automatic process.
- [ ] `dispute.register` records the case; it does not move money. Provisional credit
      is a deliberately excluded fifth command — adding it is an ADR after the pilot,
      because it changes the institution's risk position.
- [ ] `statement.request` produces a document asynchronously and delivers it through
      the notification service or a signed download link with a short TTL.

The API is narrow on purpose. Every command is a promise about a human decision, and
promises should be few and explicit.

### Hardening

- [ ] **Threat model review** — recorded as an ADR. Covers: token theft, replay,
      insider action, partner webhook forgery, SSRF, injection in narration fields,
      dependency compromise.
- [ ] **Penetration test** — external, before the pilot opens, with findings triaged.
      Critical and high findings are fixed; medium are scheduled with an owner.
- [ ] **Backup and restore drill** — restore a production-shaped database to a clean
      environment, reconcile against the known state, measure RPO and RTO. Written
      down, repeated quarterly.
- [ ] **Disaster recovery** — a documented runbook for region loss, with the tested
      restore as evidence. RPO and RTO stated and accepted by the operator.
- [ ] **Dependency and image scanning** on every build; a documented policy for
      responding to a critical CVE within 24 hours.
- [ ] **Secret rotation** — JWT signing keys, database credentials, webhook secrets,
      rail credentials. Each with a runbook and a tested rotation.
- [ ] **On-call** — a rotation, a paging policy, a severity ladder, an incident
      process, and a blameless review template.

### Data protection

- [ ] **PII is not in the ledger.** Entries carry account and transaction ids; names,
      emails and phone numbers live in `auth` and in the onboarding records. A
      subject-access request is answered from those tables.
- [ ] **Erasure does not delete entries.** A deletion request redacts PII in `auth` and
      replaces the display name with a tombstone. The ledger keeps its ids and its
      amounts, because the ledger is a record of money, not of people. This is
      documented and reviewed by counsel; the tension between append-only and erasure
      is real and is resolved by separating the two facts.
- [ ] **Retention** — notification and webhook delivery records are pruned on a
      schedule; ledger and audit records are retained per the institution's policy.
- [ ] **Access** — no human has direct database write access to money tables in
      production. Break-glass access is audited and time-boxed.

### The pilot

- [ ] A pilot plan: cohort size, limits per user, limits per day, the date, the
      rollback criterion, and the person who decides.
- [ ] Lower limits first, raised on evidence — not on schedule.
- [ ] A daily operations ritual: recon results, open breaks, settlement position,
      unresolved `Unknown`s, SLO burn. Fifteen minutes, every morning, with a written
      record.
- [ ] A feedback channel from the pilot cohort to the team, and a triage rule for what
      counts as a defect versus a feature request.
- [ ] A weekly review against the exit criteria above.

### The go/no-go for G5

Written before the pilot starts, so the decision is not made in the excitement of the
last week. It reads:

1. Four weeks, zero unexplained divergence.
2. Every SLO met for the final two weeks, or the miss has a signed acceptance.
3. No open severity-1 incident.
4. Back office ran the cycle unaided for ten consecutive business days.
5. Every runbook exercised at least once, or marked untested with a reason.
6. The operator's compliance function has signed the pilot's record.

## Data model changes

The projection's own schema (read-only, rebuildable from the event log). `disputes`
and `statements` tables in the relevant services. An `audit_log` consolidation if the
per-service audit tables need one view.

## API surface

| Method | Path | Notes |
|---|---|---|
| GET | `/v1/feed/events` | Cursor feed for the back office |
| GET | `/v1/feed/wallets/{id}` | Projected wallet view |
| POST | `/v1/commands/transfer/place-hold-for-review` | Command, `command_id` required |
| POST | `/v1/commands/transfer/release-review-hold` | Command |
| POST | `/v1/commands/dispute/register` | Command |
| POST | `/v1/commands/statement/request` | Command |
| GET | `/v1/disputes/{id}` | Case status |
| GET | `/v1/statements/{id}` | Statement status and link |

## Invariants this stage must leave true

- **Article IX** — the back office never touches the platform's database. It reads the
  feed and issues commands.
- **Article XI** — every command is attributed to a named operator with a reason.
- **Article IV** — disputes and erasure never delete entries.
- **Article XII** — the pilot's exit is evidence, written before it started.

## Tests you must write

- [ ] **Feed replay** — a consumer that disconnects and resumes from a cursor misses
      nothing and duplicates nothing it dedupes.
- [ ] **Command idempotency** — the same `command_id` twice applies once.
- [ ] **Review hold** — a review hold cannot be captured or expired by any automatic
      path; only the release command moves it.
- [ ] **Authorisation** — a command scope cannot call a different command.
- [ ] **Statement** — the statement's totals equal the sum of entries in range.
- [ ] **Restore drill** — automated as far as possible: restore, reconcile, assert
      equality.
- [ ] **Erasure** — a redacted user's PII is gone from `auth`; ledger entries are
      unchanged; the dispute history still resolves.

## Definition of done

- [ ] The go/no-go criteria are written, signed and filed before day one of the pilot.
- [ ] The pen test is closed with no open critical or high findings.
- [ ] The restore drill has been run and its RPO/RTO recorded.
- [ ] Every runbook exists and at least one person other than its author has used it.
- [ ] `reference/security.md` and `deploy/runbooks/` are complete.
- [ ] The pilot's weekly reviews are filed.

## What NOT to build yet

- **A back-office UI.** TOVARYN is the UI. Building one here duplicates it and splits
  the operator's attention.
- **Self-service partner onboarding.** Operator-created clients until the pilot shows
  the real shape of demand.
- **Multi-tenant isolation.** Single tenant through the pilot; the envelope and the
  schema carry `tenant_id` so it stays additive.
- **A fraud engine.** Fraud rules are a product with its own data. The review-hold
  command is how an external engine — including a human — acts on the platform.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Back office asks for a fifth command in week one | The four were chosen without them | The command list was not reviewed with the operators before S9 started |
| Pilot keeps hitting limits | Limits set for safety, not for evidence | Limit-raise requests outnumber feature requests |
| Break backlog grows during pilot | Recon runs but nobody owns breaks | Daily ritual skipped |
| Go/no-go becomes a formality | Criteria written after the pilot | Criteria dated after day one |
| Direct database access in production | Break-glass without audit | An unaudited psql session in production |

## Handover

There is no Stage 10. The handover is to an operations team, a runbook set, and a
programme that can decide — with evidence — whether to expand, plateau, or change
direction. That is what the whole build was for.
