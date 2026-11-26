# 0006. The posting adapter sends business operations, not GL legs

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

The payments platform keeps its own ledger and must also post to the institution's core
banking system (Apache Fineract, or the local CBA) so that the institution's chart of
accounts reflects the money the platform moved. The two systems have different ledgers:
the platform's is account-level and operation-level; the core's is a general ledger with
debit and credit legs against GL accounts.

The tempting integration is for the payments platform to construct GL legs and send them
to the core. That requires the platform to know the institution's chart of accounts, its
product mappings, and its posting rules — and to keep knowing them as they change.

## Decision

The posting adapter sends **business operations with a business-level split of entries**,
each carrying a `reference_number` and the operator-facing `operation_type`. It never
sends GL account codes, debit/credit postings to specific GL accounts, or journal
entries.

```json
{
  "operation_type": "fund-wallet",
  "amount_minor": 250000,
  "currency": "NGN",
  "value_date": "2026-11-24",
  "reference_number": "ECHO-20261124-0007X3",
  "entries": [
    { "role": "customer", "direction": "credit", "amount_minor": 250000 },
    { "role": "funding-source", "direction": "debit", "amount_minor": 250000 }
  ]
}
```

The core adapter maps `role` and `operation_type` to the institution's GL accounts using
the institution's own mapping (`account_mappings`, product configuration). The payments
platform does not own the chart of accounts.

## Alternatives considered

**Send GL legs.** Rejected: it couples the platform to the core's chart of accounts, so
every product or GL change in the core becomes a change and a redeploy in the platform,
and every test in the platform must know the GL. It also means the platform, not the
institution, is authoritative about how money maps to accounts — which is not the
platform's job.

**Send only the amount and let the core decide everything.** Rejected the other way: the
core cannot know which of its accounts a wallet funding came from, and a posting with no
business structure cannot be reconciled against the platform's ledger.

**A pre-agreed fixed mapping of operation types to GL pairs, hard-coded.** Close, but
brittle. The mapping is an institution-specific configuration, so it lives in
`account_mappings` and is onboarded per account, not hard-coded.

## Consequences

- The posting contract is stable across core implementations. The `mock`, `fineract` and
  `localcba` adapters implement one interface with one request shape.
- The core remains the book of record for the institution's GL (Article X); the payments
  platform is the book of record for the customer wallet. Neither does the other's job.
- Reconciliation compares the platform's entries against the core's statement, at the
  reference level — which is why every posting carries a unique `reference_number`.
- A GL-side mapping error is a core configuration problem, visible as a rejected or
  mismatched posting and a break, rather than a silent wrong leg.
- The adapter must translate the institution's response codes too, which is the same
  three-outcome problem as the rail, and uses the same `Applied | Rejected | Unknown`
  model (see ADR 0005).
