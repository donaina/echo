# S8 — Rail

**Weeks 18–22 · Gate G3 → G4 · Owner: integration engineer, ledger engineer**

## Purpose

Connect the platform to the instant payments rail: name enquiry before a transfer,
outbound transfers that reserve and settle, inbound transfers that credit in time,
status queries for the unknown, and per-cycle settlement that reconciles the rail
against the ledger.

This is seam D, and it is the least forgiving seam in the system: the rail imposes
timeouts measured in seconds, response codes that are not always truthful about what
happened, and a settlement cycle that must balance to the kobo.

## Depends on

S3 (holds and `Unknown`), S6 (assurance), S7 (the reconciliation service and the
posting discipline that the rail reuses).

## Exit criterion

Against the rail simulator:

1. 5,000 outbound transfers, of which 500 are made to hang and 200 to fail after
   applying. Every one reaches a terminal state; the ledger balances; no transfer is
   both failed and settled.
2. 5,000 inbound transfers, 1% of which are delivered twice by the simulator. Every
   one credits exactly once within the rail's response window.
3. A settlement cycle closes with net position equal to the sum of applied transfers,
   and the payments-to-rail reconciliation reports zero unexplained.

## Gate

**G3 → G4.** Passing G3 means the rail behaves under its own rules. G4 is operator
certification against the real rail, which is the operator's process, not this stage's.

## Why this stage exists

Because the rail is where a payments platform meets the real world: other banks,
other systems, and a switch that does not care that your service was restarting. The
designs built in S3 and S7 — holds, unknown, enquiry, reconciliation — exist precisely
so this stage is mostly integration rather than invention.

## What you build

### The rail adapter

```go
// internal/rail/port
type Rail interface {
    NameEnquiry(ctx context.Context, accountNumber string, bankCode string) (EnquiryResult, error)
    Transfer(ctx context.Context, req TransferRequest) (TransferResult, error)
    StatusQuery(ctx context.Context, req StatusRequest) (TransferResult, error)
}
```

- [ ] `TransferRequest` carries: `instruction_id` (ULID, generated once), `session_id`
      (from the rail's session), `source_account`, `destination_account`,
      `destination_bank`, `amount_minor`, `currency`, `narration`, `reference_number`,
      `name_enquiry_ref`.
- [ ] `TransferResult` is `Applied | Rejected | Unknown`, with a rail response code, a
      raw message, and the rail's transaction reference when present.
- [ ] Response codes are mapped to these three states by an explicit table, in code,
      reviewed against the rail's specification. An unmapped code is `Unknown`, never
      `Rejected` — the safe default.
- [ ] `simulator` — implementation against the provided NIP simulator (see the
      ecosystem document set, `03-Payments-Platform/03-Integration-with-TOVARYN-and-CBA.pdf`),
      with the ability to be scripted into hang, fail-after-apply and duplicate
      behaviours.
- [ ] A timeout shorter than the rail's own window, so the platform always has time to
      record `Unknown` before the switch considers the instruction abandoned.

### Sessions

- [ ] The rail runs in sessions with fixed cut-off times. The adapter signs in at
      session open, refreshes credentials, and signs out at close.
- [ ] `rail_sessions`: `session_id`, `opened_at`, `closed_at`, `state`. An instruction
      created outside a session is not sent; it waits or fails with a typed error.
- [ ] A holiday and cut-off calendar in config, versioned, with a test that the
      platform refuses to initiate outbound transfers after cut-off.

### Outbound transfer

The saga grows a rail destination, and nothing else about it changes.

```
Reserved (hold at source)
  → NameEnquiry      (if required; cache the result for the instruction's lifetime)
  → Sent             (rail accepted the instruction; unknown outcome)
  → Applied          (capture hold → entries → posted)
  → Rejected         (release hold → failed)
  → Unknown          (park → StatusQuery loop)
```

- [ ] Name enquiry happens **before** the hold in the straight-through path, so a
      wrong account is rejected without touching money. If enquiry itself is unknown,
      the transfer fails before any money moves.
- [ ] The hold is captured only on `Applied`. On `Rejected`, released. On `Unknown`,
      left open and the saga parks in `Resolving`.
- [ ] `StatusQuery` is the only mechanism that resolves an outbound unknown, and it is
      polled with backoff up to the rail's own settlement deadline; after that, the
      cycle reconciliation is the arbiter.
- [ ] A rail `Rejected` with a duplicate-reference code is treated as a **probable
      duplicate** and sent to enquiry, never blindly accepted as a failure.

### Inbound transfer

The inbound path has the tightest budget in the system. The switch expects a response
within seconds, and it may retry if it does not get one.

- [ ] The handler, in one database transaction: dedupe on `(session_id, instruction_id)`,
      validate the destination account, credit the wallet, post the settlement-account
      leg, write the outbox event, record the response. Then respond.
- [ ] No external calls in this path. No notifications, no webhooks, no core posting —
      those happen downstream from the outbox event.
- [ ] Idempotent on the rail's identity: a second delivery of the same instruction
      returns the same success response and credits nothing.
- [ ] If the transaction cannot complete within the response window, the handler
      returns a **retryable failure** and records a pending inbound the platform will
      resolve when the switch retries or when the cycle reconciles. It never returns
      success for a credit it did not write.
- [ ] An inbound to an unknown or frozen account is rejected with the appropriate
      code, and the money goes to the rail suspense account, not into the void.
- [ ] A load test at the rail's peak instruction rate is part of the exit criterion.

### Settlement

- [ ] The rail settles on a cycle. The platform maintains a **rail settlement account**
      per currency: outbound transfers debit it, inbound credits it, and the net is
      the position for the cycle.
- [ ] `settlement_cycles`: `id`, `rail`, `currency`, `business_date`, `opened_at`,
      `cutoff_at`, `closed_at`, `net_position_minor`, `status`.
- [ ] At cut-off, a job computes the net position from applied instructions and
      records it. The rail reports its own net; the reconciliation compares.
- [ ] A settlement difference is a break, and its severity is critical — it means the
      platform and the switch disagree about money that has already moved.

### Payments-to-rail reconciliation

- [ ] Per cycle, and intraday for high-value: every instruction sent is matched to a
      rail outcome; every rail credit is matched to a ledger credit.
- [ ] Uses the same `recon` service and the same break lifecycle as S7, with
      `kind = 'rail'`.
- [ ] `unmatched-outbound` (sent, no rail record), `unmatched-inbound` (rail record, no
      ledger credit), `amount-mismatch`, `duplicate-credit`.

### Conformance

- [ ] A conformance suite against the simulator: every message type, every response
      code in the mapping table, every timeout path.
- [ ] A written mapping table, `reference/api.md` → "rail codes", reviewed against the
      operator's specification and versioned.
- [ ] Operator certification is a separate activity; this stage produces the evidence
      package it needs.

## Data model changes

`rail_sessions`, `settlement_cycles`, `rail_instructions` (one per outbound and
inbound), plus the rail settlement accounts seeded per currency.

## API surface

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/transfers/external` | Outbound; saga-backed; requires `Idempotency-Key` |
| POST | `/rail/inbound` | Inbound; mTLS or IP allowlist; the switch's shape |
| GET | `/v1/settlement/cycles` | Operator |
| GET | `/v1/settlement/cycles/{id}` | Net position and reconciliation status |

## Invariants this stage must leave true

- **Article VII** — an unmapped response code is `Unknown`; a hang is `Unknown`; a
  duplicate code goes to enquiry, not to failure.
- **Article VIII** — an outbound transfer never leaves the source account debited
  without a hold lifecycle closing: captured on applied, released on rejected.
- **Article III** — inbound credits are zero-sum against the rail settlement account;
  no money enters the ledger from nowhere.
- **Article VI** — the inbound response is written after the outbox row, in the same
  transaction, so a credit that the customer can see is a credit the events announce.

## Tests you must write

- [ ] **Timeout after apply** — the simulator applies and hangs; enquiry resolves;
      exactly one credit and one capture.
- [ ] **Duplicate inbound** — the simulator delivers twice; one credit, two
      responses, both successful.
- [ ] **Wrong account name** — name enquiry fails; no hold, no money.
- [ ] **Cut-off** — an outbound after cut-off is refused with a typed error.
- [ ] **Settlement** — a cycle's net equals the sum of applied instructions, to the kobo.
- [ ] **Settlement mismatch** — the simulator under-reports; a critical break opens.
- [ ] **Response window** — inbound under peak load responds within the window for
      99.9% of instructions.
- [ ] **Frozen destination** — an inbound to a frozen account routes to rail suspense
      and is visible.

## Definition of done

- [ ] The three exit-criterion runs are recorded.
- [ ] `reference/api.md` documents the rail contract and the complete code mapping.
- [ ] The simulator is scriptable from the test suite and its version is pinned.
- [ ] Runbooks for `rail-session-down`, `settlement-mismatch`, `rail-unknown-backlog`.
- [ ] The evidence package for operator certification is assembled and listed.

## What NOT to build yet

- **Other rails.** One rail, proven. A second rail is a second set of codes and a
  second settlement cycle; it is S9's expansion, if at all.
- **Real-time settlement.** The rail's cycle determines settlement; the platform
  reflects it, it does not invent a faster one.
- **A generic rail abstraction beyond the three methods.** Generality here is
  premature; the second rail will tell you what the abstraction should be.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| Double credit on retry | Dedupe key not on the rail's identity | Duplicate inbound test fails |
| Success returned for a credit that failed | Responding before commit | An inbound response with no ledger entry |
| Settlement never balances | Rail settlement account not the only counterparty | Net position differs by the sum of fees |
| Unknown backlog grows | StatusQuery not scheduled after a timeout | `rail_unknown_backlog` rising |

## Handover

Stage 9 connects the platform to the back office, hardens everything for a pilot, and
runs it. The seams are all built; the last job is to make them operable by people who
are not the people who built them.
