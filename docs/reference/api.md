# API

The platform's HTTP surface: conventions that apply everywhere, then the endpoints
grouped by service. The OpenAPI files in `api/openapi/` are the machine-readable
truth; this document is the reasoning and the parts a schema cannot express.

---

## Conventions

| Concern | Rule |
|---|---|
| Base path | `/v1` for everything public; `/internal` for service-to-service |
| Content type | `application/json`; errors use `application/problem+json` |
| Encoding | UTF-8, no BOM |
| Money | `amount_minor` as a JSON number (integer), `currency` as an ISO 4217 code |
| Time | RFC 3339, UTC, always with an offset |
| Dates | `YYYY-MM-DD`, business dates |
| IDs | ULIDs, strings, case-sensitive |
| Idempotency | `Idempotency-Key` header on every money write |
| Correlation | `X-Request-Id` echoed; `traceparent` propagated |
| Pagination | Cursor-based, opaque cursor |
| Errors | RFC 9457 problem+json, stable `code` |
| Versioning | Path version for breaking changes; additive changes do not bump |

### Idempotency

Every endpoint that can move money or create money-bearing state requires an
`Idempotency-Key`.

- The key is scoped to the authenticated principal and the route.
- Same key, same body → the original response is replayed with
  `Idempotent-Replayed: true`.
- Same key, different body → `422 idempotency_key_reused`.
- Same key while the first is still running → `409 request_in_progress`.
- Keys live 24 hours by default. Clients must not retry beyond that window with the
  same key expecting replay; they must query the resource instead.

The key is not a nonce. It identifies *this intent*, and the same intent retried is the
same key. See `reference/errors.md` for the exact codes.

```http
POST /v1/transfers/internal
Idempotency-Key: 0192f3c4-7a1b-7c9d-9e0f-2b3c4d5e6f70
Authorization: Bearer ...
Content-Type: application/json

{
  "from_account": "01J8Z...",
  "to_account":   "01J9A...",
  "amount_minor": 250000,
  "currency":     "NGN",
  "reference":    "rent-november"
}
```

```http
201 Created
Idempotent-Replayed: false
Location: /v1/transfers/01J9B...

{
  "transaction_id": "01J9B...",
  "status": "posted",
  "from_account": "01J8Z...",
  "to_account": "01J9A...",
  "amount_minor": 250000,
  "currency": "NGN",
  "occurred_at": "2026-11-24T09:31:07.412Z",
  "value_date": "2026-11-24"
}
```

### Pagination

Cursor pagination only. A cursor is an opaque string that encodes the sort position;
clients must not construct or interpret it.

```http
GET /v1/wallets/01J8Z.../entries?limit=50&cursor=eyJ0IjoiMjAyNi0xMS0yNFQwOTozMSJ9
```

```json
{
  "items": [ ... ],
  "next_cursor": "eyJ0IjoiMjAyNi0xMS0yNFQwOTozMCJ9",
  "has_more": true
}
```

Cursors are stable across identical queries and survive inserts. A cursor from a
different filter is rejected with `400 invalid_cursor` rather than silently returning
the wrong page.

### Async operations

Operations that cannot complete in the request (saga transfers, statements) return
`202 Accepted` with a resource id and a `Location` header. The client polls the
resource or waits for the webhook. There is no long-poll and no callback URL on the
request.

```json
{
  "id": "01J9B...",
  "status": "received",
  "links": { "self": "/v1/transfers/01J9B..." }
}
```

### Versioning

- A field is added: no version bump.
- A field is deprecated: it stays, marked in OpenAPI, with a sunset date at least 90
  days out.
- A field is removed or its meaning changes: new path version (`/v2`), both served
  during a migration window.

## Endpoints

### Auth (`/v1/auth`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/register` | none | Create a user and credential |
| POST | `/login` | none | Issue access and refresh tokens |
| POST | `/refresh` | refresh cookie | Rotate tokens |
| POST | `/logout` | bearer | Revoke the session |
| GET | `/.well-known/jwks.json` | none | Signing keys |

Login response:

```json
{
  "access_token": "eyJ...",
  "token_type": "Bearer",
  "expires_in": 900,
  "refresh_token": "opaque...",
  "user": { "id": "01J...", "email": "a@example.com" }
}
```

The refresh token is also set as an `HttpOnly; Secure; SameSite=Strict` cookie. It is
returned in the body only for non-browser clients, controlled by the request's
`Accept` semantics — a browser client uses the cookie and never sees the token in
JavaScript.

### Wallet (`/v1/wallets`)

**Stage 2 boundary.** The money endpoints below are served on the wallet's **internal
port**, not the public edge, behind `X-Echo-Service-Token` and `X-Echo-Caller`. They
are **not idempotent** until Stage 3: there is no `Idempotency-Key` yet, so a retried
request is a second movement. Do not publish these routes before the gateway and
idempotency land. The `Auth` column describes the target state from Stage 4.

| Method | Path | Auth | Idempotent | Purpose |
|---|---|---|---|---|
| GET | `/{id}` | owner | — | Balances and status |
| GET | `/{id}/entries` | owner | — | Entry history |
| GET | `/{id}/holds` | owner | — | Open holds |
| POST | `/{id}/fund` | owner or operator | required | Credit a wallet |
| POST | `/{id}/withdraw` | owner | required | Debit a wallet |

Wallet response:

```json
{
  "id": "01J8Z...",
  "status": "active",
  "currency": "NGN",
  "ledger_balance": 1250000,
  "available_balance": 1150000,
  "open_holds": 100000,
  "created_at": "2026-10-01T08:00:00Z"
}
```

`ledger_balance − available_balance` is always the sum of open holds. If a client sees
otherwise, it is a bug and should be reported with both values.

Entries response (Stage 2 shape; newest first, cursor on `created_at` and `id`):

```json
{
  "items": [
    {
      "id": "01J9D...",
      "transaction_id": "01J9C...",
      "operation_type": "internal-transfer",
      "delta_minor": -25000,
      "currency": "NGN",
      "reference": "rent-november",
      "counterparty": "01J8Y...",
      "created_at": "2026-11-24T09:31:07.412Z"
    }
  ],
  "next_cursor": "eyJ0IjoiMjAyNi0xMS0yNFQwOTozMSJ9",
  "has_more": true
}
```

`counterparty` is the account on the other side of the transaction, and is empty for a
transaction whose legs all touch the same account.

### Transfers (`/v1/transfers`)

| Method | Path | Auth | Idempotent | Returns |
|---|---|---|---|---|
| POST | `/internal` | owner of source | required | `201` posted |
| POST | `/external` | owner of source | required | `202` accepted |
| GET | `/{id}` | owner | — | Saga or transaction state |

Transfer states reported by `GET /v1/transfers/{id}`:

| State | Meaning | Terminal |
|---|---|---|
| `received` | Accepted, saga created | no |
| `reserved` | Funds held at source | no |
| `sent` | Handed to the rail | no |
| `completed` | Money moved, both sides | yes |
| `failed` | No money moved, hold released | yes |
| `unknown` | Outcome not yet known; resolving | no |
| `reversed` | Moved, then reversed with evidence | yes |

`unknown` is never collapsed into `failed`. A client that retries on `failed` is
correct; a client that retries on `unknown` will receive `409` until the platform
resolves it.

### Disputes and statements (`/v1/commands`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/dispute/register` | operator scope | Create a dispute case |
| POST | `/statement/request` | operator scope | Request a statement |
| POST | `/transfer/place-hold-for-review` | operator scope | Review hold |
| POST | `/transfer/release-review-hold` | operator scope | Release review hold |

Every command requires `command_id` (an idempotency key) and a `reason`. The actor is
taken from the authenticated service identity, never from the body.

### Feed (`/v1/feed`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/events` | operator/partner read scope | Ordered event feed |
| GET | `/wallets/{id}` | operator read scope | Projected wallet view |

Feed response:

```json
{
  "items": [
    {
      "event_id": "01J...",
      "type": "transfer.completed",
      "occurred_at": "2026-11-24T09:31:07.412Z",
      "data": { ... }
    }
  ],
  "next_cursor": "...",
  "as_of": "2026-11-24T09:32:00Z"
}
```

`as_of` is the projection's watermark. The feed is eventually consistent by design.

### Settlement and reconciliation (`/v1/settlement`, `/v1/recon`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/v1/settlement/cycles` | operator | Cycle list |
| GET | `/v1/settlement/cycles/{id}` | operator | Net position, status |
| GET | `/v1/recon/runs` | operator | Run list |
| GET | `/v1/recon/breaks` | operator | Break queue |
| POST | `/v1/recon/breaks/{id}/assign` | operator | Assign |
| POST | `/v1/recon/breaks/{id}/resolve` | operator | Resolve with evidence |

## The posting contract (wallet → core)

Internal, but specified here because it is a contract between two systems.

```
POST /internal/posting/operations
Idempotency-Key: <operation_id>
```

```json
{
  "operation_id": "01J...",
  "idempotency_key": "01J...",
  "operation_type": "fund-wallet",
  "account_ref": "01J8Z...",
  "core_account_ref": "000123456",
  "amount_minor": 250000,
  "currency": "NGN",
  "value_date": "2026-11-24",
  "reference_number": "ECHO-20261124-0007X3",
  "narration": "Wallet funding",
  "entries": [
    { "role": "customer", "direction": "credit", "amount_minor": 250000 },
    { "role": "funding-source", "direction": "debit", "amount_minor": 250000 }
  ]
}
```

Response:

```json
{
  "status": "posted",
  "core_reference": "FT2026112400123",
  "booked_at": "2026-11-24T09:31:09Z"
}
```

`status` is `posted`, `rejected` or `unknown`. A `rejected` response carries a
`code` and a `detail`. An `unknown` response must be followed by an enquiry on
`reference_number`; the core must never apply the same `idempotency_key` twice.

## The rail contract (payments → NIP)

Three operations, as specified in S8. Codes map to `applied`, `rejected` or
`unknown` through a table in `internal/rail/nip/codes.go`, reviewed against the
operator's specification.

| Rail message | Platform need | Timeout |
|---|---|---|
| Name Enquiry | Validate the destination before holding funds | 10 s |
| Fund Transfer | Move money; outcome may be applied, rejected or unknown | 30 s |
| Status Query | Resolve an unknown; the only trusted source | 15 s |

An unmapped response code is `unknown`. This is a deliberate asymmetry: misreading a
success as a failure costs money, and misreading a failure as unknown costs a query.

## Rate limits

Applied at the gateway, per principal and per route class.

| Class | Limit | Burst |
|---|---|---|
| `auth:login` | 10 / minute per IP and per email | 3 |
| `auth:register` | 5 / hour per IP | 2 |
| `wallet:read` | 300 / minute | 50 |
| `wallet:write` | 60 / minute | 10 |
| `transfer:create` | 30 / minute | 5 |
| `feed:read` | 600 / minute | 100 |

Exceeded limits return `429` with `Retry-After` in seconds and a problem+json body
carrying the limit that was hit.

## What the API deliberately does not offer

- **A balance from the core.** Article X. The platform's balance is the platform's.
- **A synchronous external transfer.** The rail cannot promise one, so the API does
  not pretend to.
- **Server-side pagination by offset.** Offsets drift under inserts and produce
  duplicates and omissions. Cursors only.
- **A public endpoint that lists all accounts.** Enumeration is an attack; the operator
  surface lists them with a scope, the customer surface never does.
