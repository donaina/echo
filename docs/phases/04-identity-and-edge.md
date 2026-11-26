# S4 — Identity and edge

**Weeks 9–11 · Gate G3 · Owner: security engineer, service engineer**

## Purpose

Open the platform to users without opening it to abuse. Build the identity service
(accounts, credentials, tokens), the gateway that is the only public surface, and the
authorisation rule that makes every earlier endpoint safe for a real customer rather
than a test.

By the end of this stage, the internal service token is deleted and nothing reaches a
wallet except a request that proved who it was.

## Depends on

S3. Idempotent, saga-safe money writes.

## Exit criterion

A user registers, logs in, and moves money in their own wallet. The same user's token
is used to attempt a withdrawal from another user's wallet and receives `403`, with no
state change. The gateway is the only port reachable from outside the network, and a
port scan from the test client confirms it. An access token that has been rotated
stops working within its TTL.

## Gate

**G3 — synthetic release readiness.** The perimeter is real. From here, every stage
must pass a security review before its exit criterion is signed.

## Why this stage exists

Because at S3 the platform could move money but only for a caller that could reach an
internal port. That is not a product; it is a liability. S4 also exists to enforce a
separation that is easy to get wrong — the gateway may verify a token, but only the
wallet service may decide whether that token may touch that account.

## What you build

### The auth service

```sql
CREATE TABLE users (
  id                TEXT PRIMARY KEY,      -- ULID; this is the owner_ref wallets use
  email             TEXT NOT NULL,
  email_normalised  TEXT NOT NULL,         -- lowercased, trimmed
  phone             TEXT,
  status            TEXT NOT NULL,         -- 'pending' | 'active' | 'locked' | 'closed'
  email_verified_at TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL,
  updated_at        TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX users_email_uq ON users (email_normalised);

CREATE TABLE credentials (
  user_id        TEXT PRIMARY KEY REFERENCES users(id),
  password_hash  TEXT NOT NULL,            -- argon2id, parameters recorded in the hash string
  password_set_at TIMESTAMPTZ NOT NULL,
  failed_attempts INT NOT NULL DEFAULT 0,
  locked_until   TIMESTAMPTZ
);

CREATE TABLE refresh_tokens (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id),
  token_hash    TEXT NOT NULL,             -- SHA-256 of the opaque token; never the token
  family_id     TEXT NOT NULL,             -- rotation family, for reuse detection
  issued_at     TIMESTAMPTZ NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  revoked_at    TIMESTAMPTZ,
  replaced_by   TEXT REFERENCES refresh_tokens(id),
  user_agent    TEXT,
  ip_hash       TEXT
);
CREATE UNIQUE INDEX refresh_tokens_hash_uq ON refresh_tokens (token_hash);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
```

- [ ] `POST /auth/register` — creates a user and a credential. Password policy: length
      over composition (12+ characters, breached-password check offline, no forced
      symbols). Email normalisation is explicit and tested.
- [ ] `POST /auth/login` — constant-time verification (verify a dummy hash when the
      user does not exist, so timing does not reveal existence). Returns an access
      token and a refresh token.
- [ ] `POST /auth/refresh` — rotates the refresh token. **Reuse detection:** if a
      refresh token that has already been replaced is presented, revoke the whole
      family and force re-login. This is the standard protection against a stolen
      token being replayed.
- [ ] `POST /auth/logout` — revokes the presented refresh token and its family.
- [ ] `GET /auth/.well-known/jwks.json` — the signing keys, public halves only.
- [ ] `POST /auth/introspect` — internal-only; used by services that need the current
      session state for a revoked access token (short-TTL denylist).

### Token design

| Property | Value | Why |
|---|---|---|
| Algorithm | EdDSA (Ed25519) | Small keys, fast verification, no padding-mode footguns |
| Access token TTL | 15 minutes | Short enough that revocation lag is tolerable |
| Refresh token TTL | 30 days, rotating | Long-lived convenience without a permanent bearer |
| Claims | `sub` (user id), `sid` (session), `iat`, `exp`, `iss`, `aud`, `scope` | No email, no role soup — the wallet resolves what it needs |
| Storage | Access token in memory on the client; refresh token in an `HttpOnly` cookie | Never `localStorage` |
| Revocation | Session denylist in Redis, keyed by `sid`, TTL = access token TTL | Redis is used for this and rate limits only |

Key management: signing keys are loaded from configuration with a `kid`; rotation
publishes the new key in JWKS before it signs, and retires the old one after the
longest token TTL (see `reference/security.md`).

### The gateway

The only public surface. It holds no money state.

- [ ] Routing: `/v1/auth/*` → auth; `/v1/wallets/*`, `/v1/transfers/*` → the
      corresponding service; everything else → `404` with a problem+json body.
- [ ] Token verification: signature against JWKS (cached), `exp`, `iss`, `aud`. It
      does **not** make authorisation decisions about accounts — it establishes
      identity and forwards it as a signed internal header plus the original token.
- [ ] Rate limiting in Redis: token bucket per `sub` and per route class
      (`auth:*` is stricter than `wallet:read`, which is stricter than `wallet:write`).
      `429` with `Retry-After`.
- [ ] Body size limits, header limits, request timeouts (shorter than the downstream
      service's, so the gateway is never the last to give up).
- [ ] `X-Request-Id` generation and propagation; `traceparent` propagation.
- [ ] No request logging of bodies on money routes; never logs a token.

### Authorisation in the wallet

This is the part that matters most, and it is the part a gateway cannot do.

- [ ] The wallet service derives caller identity from the **validated internal header
      signed by the gateway**, not from a body field or a query parameter.
- [ ] A wallet account's `owner_ref` is a user id. A request to debit wallet *W* is
      permitted only if `caller.sub == W.owner_ref` (or the caller holds a service
      scope for the operation, e.g. an operator-initiated fund).
- [ ] The authorisation check happens **in the same transaction** as the posting, or
      before it, against a freshly loaded account row — never from a cached projection.
- [ ] A missing or ambiguous owner on an account fails closed (`403`).

- [ ] The temporary service token from S2 is **deleted**, not merely unused. A test
      asserts it is not accepted.

## Data model changes

auth: `users`, `credentials`, `refresh_tokens`. wallet: no schema change; `owner_ref`
becomes meaningful (a user id) and gains a validation that it is a ULID.

## API surface

| Method | Path | Port | Auth |
|---|---|---|---|
| POST | `/v1/auth/register` | public | none |
| POST | `/v1/auth/login` | public | none |
| POST | `/v1/auth/refresh` | public | refresh cookie |
| POST | `/v1/auth/logout` | public | access token |
| GET | `/v1/auth/.well-known/jwks.json` | public | none |
| POST | `/v1/auth/introspect` | internal | service token |
| POST | `/v1/wallets/{id}/fund` | public | access token + owner or operator scope |
| POST | `/v1/wallets/{id}/withdraw` | public | access token + owner |
| POST | `/v1/transfers/internal` | public | access token + owner of source |
| POST | `/v1/transfers` | public | access token + owner of source |
| GET | `/v1/wallets/{id}` | public | access token + owner |
| GET | `/v1/wallets/{id}/entries` | public | access token + owner |

## Invariants this stage must leave true

- **Article IX** — the gateway holds no money state; the wallet owns authorisation.
- **Article XI** — `created_by` is now a user id or a service identity, and either way
  it is real and verifiable.
- **Article I** (indirect) — no token, secret or credential is ever logged or stored in
  a money table.
- **Article V** — public money routes still require an `Idempotency-Key`, now scoped to
  the user so one user's key cannot collide with another's.

## Tests you must write

- [ ] **Cross-user access** — the exit criterion, as a test: `403`, no state change.
- [ ] **Constant-time login** — a statistical test over many samples comparing
      existing-user and non-existing-user responses (bounded, non-flaky: assert the
      means are within a tolerance rather than an exact equality).
- [ ] **Refresh rotation** — a token rotates; the old one no longer works; a reused old
      token revokes the family.
- [ ] **Expired access token** — rejected at the gateway with `401`.
- [ ] **JWKS rotation** — a token signed with the retiring key still verifies until its
      TTL elapses, then fails.
- [ ] **Rate limit** — exceeding the bucket returns `429` with a sane `Retry-After`.
- [ ] **Service token removal** — the S2 token is rejected everywhere.
- [ ] **Fuzz the token parser** — arbitrary bytes in `Authorization` never panic or
      reach the domain.

## Definition of done

- [ ] Only the gateway port is exposed by the deployment manifests.
- [ ] A security review of the auth service and gateway is recorded as an ADR or a
      review note.
- [ ] Secrets are loaded from the environment or a secret store; none are in the repo.
      A secret scanner runs in CI.
- [ ] `reference/security.md` documents the token model, key rotation and rate limits.
- [ ] The S2 service token is deleted with a commit message that says why.

## What NOT to build yet

- **Multi-factor authentication.** It is a product requirement eventually, and it is
  not the one that stops the first incident. Sequence it after the pilot; record it in
  the risk register.
- **OAuth/OIDC for third parties.** Partner access is S9's, and it is built on the
  same token primitives rather than bolted on.
- **Role-based access control beyond owner/operator.** Two roles now; a role engine
  later if the console proves it needs one.
- **A user-facing password reset flow built on email.** Email delivery is S5.

## Failure modes

| Symptom | Cause | Tell |
|---|---|---|
| One user reads another's wallet | Authorisation in the gateway only | The cross-user test is not in CI |
| Revoked token still works | Revocation only at refresh | Access token TTL longer than intended, or the denylist is not consulted |
| Login timing reveals accounts | No dummy verification | A timing distribution test shows a gap |
| Refresh token reuse undetected | No family tracking | A stolen token works twice |
| Gateway is a bottleneck | Rate limiting per request with a round trip to Redis | p99 dominated by Redis latency; use a local token bucket with a periodic sync |

## Handover

Stage 5 adds delivery: events leave the platform and reach partners and users. The
outbox already exists; S5 adds the relay, the consumers, notification and webhook
delivery, and the reason to care about consumer idempotency.
