# 0003. Money is `int64` minor units

Status: Accepted
Date: 2026-11-24
Deciders: Platform team

## Context

Every amount in the system — a wallet balance, a ledger entry, a transfer, a fee, a
settlement net position — has to be represented in a way that sums exactly. The three
common representations are floating point, decimal, and integer minor units. Two of the
three are wrong for a ledger.

## Decision

Every amount is an **`int64` in the currency's minor unit**: kobo for NGN, cents for USD,
and so on. The column is named `amount_minor` or `delta_minor`. A currency code always
travels with the amount. Conversion to a display string exists in exactly one function
per service, at the edge.

## Alternatives considered

**`float64`.** Rejected absolutely. `0.1 + 0.2 != 0.3`, and in a ledger that difference
is money that does not reconcile. No amount of rounding discipline fixes a representation
that cannot represent the values it is given.

**Decimal types (`numeric` in Postgres, a decimal library in Go).** Correct, and
rejected on grounds of friction rather than correctness: `numeric` is slower to
aggregate, the Go side needs a third-party type to compare and sum, and every boundary
(JSON, SQL, code) needs a conversion. The ledger's operation is addition, and `int64`
adds exactly.

**String amounts.** Rejected: every consumer has to parse, and a parse error in a money
path is a defect waiting for the right input.

## The boundaries

- **Wire:** JSON number, integer. Not a string, not a decimal.
- **Database:** `BIGINT NOT NULL`.
- **Go:** `int64` with a named type (`type AmountMinor int64`) so it cannot be confused
  with a count or an id.
- **Display:** one function, `FormatMinor(amount int64, currency string) string`, and no
  other code path formats an amount for a human.

## Consequences

- Sums are exact and associative; reconciliation is arithmetic rather than tolerance
  comparison.
- `int64` overflows at ~9.2 × 10¹⁸ minor units, which is ~9.2 × 10¹³ NGN — far beyond any
  realistic single account, and bounds-checked on input regardless.
- Currencies with zero decimal places (JPY) work naturally: the minor unit is the unit.
- Currencies with three decimal places (KWD, BHD) work naturally too. A system that
  hard-codes two decimals does not, and that is the bug this decision avoids.
- Display conversion is a single point of failure, which is testable in one place. That
  is better than many points of failure that are tested nowhere.
- Article I. A lint rule forbids `float` and `decimal` types in any package under
  `internal/ledger`.
