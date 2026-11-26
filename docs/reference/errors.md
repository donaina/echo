# Errors

An error in a money system is a fact the caller must act on. It has to say precisely
what happened, whether anything moved, and what to do next.

This document defines the error model, the stable codes, and the rules that keep both
honest.

---

## The model

Every error is one of three things, and the distinction matters more than the message:

| Kind | Meaning | Caller should |
|---|---|---|
| **Rejection** | The request was understood and refused. Nothing moved. | Fix the request, or stop |
| **Failure** | Something broke. Nothing moved, or the move was compensated. | Retry with backoff, same idempotency key |
| **Unknown** | The outcome is not known. Money may have moved. | **Not** retry blindly; query the resource |

A rejection and a failure can both be retried safely. An unknown cannot. Collapsing
unknown into either is the defect that this document exists to prevent.

## The wire format

RFC 9457 problem+json.

```http
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/problem+json

{
  "type": "https://errors.echo.dev/insufficient-funds",
  "title": "Insufficient funds",
  "status": 422,
  "code": "insufficient_funds",
  "detail": "The available balance (115000) is less than the requested amount (250000).",
  "instance": "/v1/wallets/01J8Z.../withdraw",
  "request_id": "req_01J9C...",
  "meta": {
    "available_minor": 115000,
    "requested_minor": 250000,
    "currency": "NGN"
  }
}
```

- `code` is the stable identifier. Clients key on it. It never changes meaning.
- `title` is a short human summary; it may be localised later.
- `detail` is for a developer, not a customer-facing string.
- `meta` holds structured context — never a raw stack trace, never an internal id the
  caller cannot use.

A `5xx` never includes `meta` with internal state. A `4xx` may.

## Status mapping

| Code | HTTP | Kind | Meaning |
|---|---|---|---|
| `validation_failed` | 400 | Rejection | Malformed or missing field |
| `invalid_cursor` | 400 | Rejection | Cursor does not match the query |
| `idempotency_key_required` | 400 | Rejection | Money write without a key |
| `unauthenticated` | 401 | Rejection | No or invalid credential |
| `token_expired` | 401 | Rejection | Access token past `exp`, refresh may work |
| `forbidden` | 403 | Rejection | Authenticated but not permitted |
| `account_not_found` | 404 | Rejection | Account id unknown to the caller |
| `transfer_not_found` | 404 | Rejection | Transfer id unknown |
| `method_not_allowed` | 405 | Rejection | Wrong verb |
| `conflict` | 409 | Rejection | State conflict (see the specific codes) |
| `request_in_progress` | 409 | Rejection | The original request is still running |
| `insufficient_funds` | 422 | Rejection | Available balance < amount |
| `account_frozen` | 422 | Rejection | Account cannot be debited |
| `account_closed` | 422 | Rejection | Account cannot transact |
| `currency_mismatch` | 422 | Rejection | Legs or request disagree in currency |
| `self_transfer` | 422 | Rejection | Source and destination are the same |
| `idempotency_key_reused` | 422 | Rejection | Same key, different body |
| `amount_out_of_range` | 422 | Rejection | Zero, negative, or beyond limits |
| `rate_limited` | 429 | Rejection | Limit exceeded; see `Retry-After` |
| `internal_error` | 500 | Failure | Unexpected; retryable with backoff |
| `dependency_unavailable` | 503 | Failure | Downstream down; retryable |
| `outcome_unknown` | 503 | **Unknown** | Money may have moved; query the resource |

## The codes that matter most

### `insufficient_funds`

Always carries `available_minor`, `requested_minor` and `currency` in `meta`. A client
should not need to call the balance endpoint to understand a refusal, and a refusal
that does not say how much was available causes a bad user experience for a tiny saving.

### `outcome_unknown`

Returned when the platform itself cannot say whether a money movement happened. It is
`503` because it is not the client's fault and the request may succeed later.

```json
{
  "type": "https://errors.echo.dev/outcome-unknown",
  "title": "Outcome unknown",
  "status": 503,
  "code": "outcome_unknown",
  "detail": "The transfer was accepted but its outcome is not yet known. Do not retry; query the transfer.",
  "meta": {
    "transfer_id": "01J9B...",
    "poll_after_seconds": 30
  }
}
```

The `transfer_id` is the instruction: query it, do not resubmit it. The client still
has its idempotency key, so even a mistaken resubmission is safe — but the correct
behaviour is a query.

### `idempotency_key_reused`

The client used one key for two different requests. This is always a client bug, and it
is always caught: the alternative is executing a different operation under a key that
promises it is the same one.

`meta` carries `original_request_fingerprint` and `received_request_fingerprint` so the
client can see what differed without guessing.

## Internally

Go code returns a typed error, never a formatted string in a money path.

```go
// internal/platform/errors
type Error struct {
    Code    Code       // stable, from the enum
    Status  int        // HTTP status
    Message string     // safe for a developer
    Meta    map[string]any
    cause   error
}

func (e *Error) Error() string { ... }
func (e *Error) Unwrap() error { return e.cause }
```

Rules:

- Every domain error is constructed from the code enum. `errors.New("nope")` is not a
  domain error.
- An unexpected error is wrapped as `internal_error` at the transport boundary, logged
  with its stack, and returned without the stack.
- A context deadline on a money call maps to `Unknown`, never to a failure code.
  **(Article VII.)** A lint rule checks this: `context.DeadlineExceeded` may not appear
  in a `return` with a non-unknown code.
- Errors carry no PII in `Message` or `Meta`; identifiers only.

## Retry guidance for clients

| Response | Retry? | How |
|---|---|---|
| 429 | Yes | After `Retry-After`, same key |
| 500, 502, 503 (not `outcome_unknown`) | Yes | Exponential backoff with jitter, same key |
| `outcome_unknown` | No | Query the resource |
| 409 `request_in_progress` | Yes, later | Same key, after a delay |
| 4xx rejection | No | Fix the request |
| Network timeout | Yes, same key | The key makes it safe |

The rule that makes all of this work: **the client never invents a new key for a
retry.** A new key is a new intent, and a new intent into a system that already did the
first one is how double debits happen.

## Operator-facing errors

The operator surface returns the same problem+json, with the same codes, but `meta` may
include the internal reference and the runbook link.

```json
{
  "code": "posting_rejected",
  "status": 409,
  "title": "Core rejected the posting",
  "meta": {
    "reference_number": "ECHO-20261124-0007X3",
    "core_code": "ACCOUNT_BLOCKED",
    "break_id": "01J9D...",
    "runbook": "deploy/runbooks/posting-rejected.md"
  }
}
```

A runbook link in an error is worth more than a paragraph in a wiki nobody opens
during an incident.

The operator surface adds these codes to the list above. They never appear on a
customer-facing endpoint.

| Code | HTTP | Kind | Meaning |
|---|---|---|---|
| `command_scope_denied` | 403 | Rejection | The operator's scope does not cover this command |
| `command_reason_required` | 400 | Rejection | A command arrived without a `reason` |
| `posting_rejected` | 409 | Rejection | The core refused a posting; a break is open |
| `break_not_found` | 404 | Rejection | Break id unknown |
| `break_invalid_transition` | 409 | Rejection | The break is not in a state that permits this action |
| `settlement_cycle_open` | 409 | Rejection | The requested action is not valid before cut-off |

`internal_error`, `dependency_unavailable` and `outcome_unknown` mean exactly what they
mean everywhere else. An operator seeing `outcome_unknown` on a posting is being told to
enquire, not to retry.

## Changing codes

A code is part of the public API. Renaming one is a breaking change and follows the
versioning rules in `reference/api.md`.

Adding a code is additive, but it still needs: an entry here, a test that the mapping
is exhaustive, and a line in the OpenAPI enum.

The CI check is simple: every `Code` constant in `internal/platform/errors` appears in
this document, and every code in this document exists in the enum. Drift fails the
build.
