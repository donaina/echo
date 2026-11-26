# Data model

The catalogue of what each service stores, who owns it, and what may be read across a
boundary.

Full DDL lives in `db/<service>/` as migrations, which are the executable truth. This
document is the map — if the map and the migration disagree, the migration wins and
this document is corrected in the same pull request.

---

## Ownership

One writer per fact. The table below is the complete list of writers.

| Fact | Owner | Never written by |
|---|---|---|
| Users, credentials, sessions | `auth` | anything |
| Accounts, entries, transactions, holds, checkpoints | `wallet` | anything |
| Posting state, account mappings | `wallet` | anything |
| Transfer lifecycle, saga state | `transfer` | anything |
| Notification delivery | `notification` | anything |
| Webhook subscriptions and deliveries | `webhook` | anything |
| Reconciliation runs and breaks | `recon` | anything |
| Event declarations (outbox) | the emitting service | anything |

Reads across a boundary happen in exactly two ways: an HTTP/gRPC call to the owner, or
a projection built from that owner's events. There is no third way, and no shared
database.

## `auth`

| Table | Key columns | Retention |
|---|---|---|
| `users` | `id`, `email_normalised` (unique), `phone`, `status` | Redacted on erasure, row kept |
| `credentials` | `user_id`, `password_hash`, `failed_attempts`, `locked_until` | Deleted with the user |
| `refresh_tokens` | `token_hash` (unique), `family_id`, `expires_at`, `revoked_at` | Pruned 90 days after expiry |
| `sessions` | `sid`, `user_id`, `revoked_at` | Same |
| `key_versions` | `kid`, `algorithm`, `public_key`, `activated_at`, `retired_at` | Kept forever |

`users.id` is the `owner_ref` that wallets use. It is the only identifier that crosses
from identity into money, and the wallet treats it as opaque.

## `wallet`

| Table | Key columns | Notes |
|---|---|---|
| `accounts` | `id`, `owner_ref`, `account_type`, `currency`, `status`, `version` | See `ledger-model.md` |
| `transactions` | `id`, `operation_type`, `status`, `occurred_at`, `value_date`, `reference`, `reverses_id` | Append-only except `status` and reversal link |
| `entries` | `id`, `transaction_id`, `account_id`, `delta_minor`, `currency` | Append-only; no update, no delete grant |
| `holds` | `id`, `account_id`, `amount_minor`, `status`, `expires_at`, `transaction_id` | Lifecycle in `ledger-model.md` |
| `balance_checkpoints` | `account_id`, `as_of_entry_id`, `balance_minor` | Verified cache; deletable |
| `idempotency_keys` | `scope`, `idempotency_key` (unique together), `request_fingerprint`, `status` | Pruned 24h + retry window |
| `postings` | `transaction_id`, `reference_number` (unique), `status`, `attempts`, `core_ref` | One per transaction that must post |
| `account_mappings` | `payments_account_id`, `core_account_ref`, `core_product`, `status` | One per account that posts |
| `outbox` | `aggregate_type`, `aggregate_id`, `event_type`, `payload`, `published_at` | Pruned 7 days after publish |

Indexes that matter:

- `entries (account_id, created_at, id)` — derivation and statement ranges
- `entries (transaction_id)` — the zero-sum check and reversal construction
- `holds (account_id) WHERE status = 'open'` — the available-balance calculation
- `outbox (created_at) WHERE published_at IS NULL` — the relay

## `transfer`

| Table | Key columns | Notes |
|---|---|---|
| `transfer_sagas` | `id`, `state`, `request` (JSONB), `hold_id`, `attempts`, `next_attempt_at`, `remote_ref` | State machine in S3 |
| `saga_steps` | `saga_id`, `step`, `started_at`, `finished_at`, `outcome`, `detail` | Append-only history; the audit trail of a saga |
| `inbox` | `(consumer_name, event_id)` primary key | Consumer idempotency |
| `outbox` | as above | |

The saga's `request` JSONB is the original instruction, stored so a resume does not
depend on the caller still being there. It contains no credentials.

## `notification`

| Table | Key columns | Notes |
|---|---|---|
| `notifications` | `id`, `user_id`, `channel`, `template`, `payload`, `status`, `attempts`, `next_attempt_at` | `payload` contains no secrets |
| `templates` | `name`, `channel`, `version`, `body` | Versioned; a change is a new version |
| `preferences` | `(user_id, channel)`, `opted_out`, `updated_at` | Transactional messages ignore opt-out |
| `provider_events` | `notification_id`, `provider`, `status`, `received_at` | Delivery receipts |

## `webhook`

| Table | Key columns | Notes |
|---|---|---|
| `subscriptions` | `id`, `partner_id`, `url`, `event_types`, `secret_ciphertext`, `status` | SSRF-validated URL |
| `deliveries` | `id`, `subscription_id`, `event_id`, `attempts`, `last_status`, `next_attempt_at`, `delivered_at` | One per event per subscription |
| `delivery_attempts` | `delivery_id`, `attempt`, `status_code`, `latency_ms`, `error` | Append-only |
| `inbox` | `(consumer_name, event_id)` | |

## `recon`

| Table | Key columns | Notes |
|---|---|---|
| `recon_runs` | `id`, `kind`, `business_date`, `started_at`, `finished_at`, counts | One per run |
| `breaks` | `id`, `run_id`, `kind`, `severity`, `account_ref`, `reference_number`, `amount_minor`, `status`, `assigned_to` | The case |
| `break_events` | `break_id`, `at`, `actor`, `from_status`, `to_status`, `note`, `evidence` | Append-only history |

`recon` writes no money. It reads entries from its own projection, and core/rail
statements from the adapters. If `recon` ever wants to fix something, that is a
command to another service, not a write here.

## The event store and projections

Projections are derived, rebuildable and unowned. A projection table may be dropped and
rebuilt from the event log at any time; anything that cannot be rebuilt is not a
projection, it is a source of truth, and it belongs to a service.

- The back office projection (S9) is the main one.
- Projection tables are named `proj_*` and live in a separate schema from service
  tables. A migration that touches `proj_*` never touches a source table.

## Cross-service references

A service may store an identifier owned by another service. It may not store a copy of
that service's mutable data as if it were its own.

| Stored in | References | Allowed to store |
|---|---|---|
| `wallet.accounts.owner_ref` | `auth.users.id` | The id only |
| `transfer_sagas.request` | wallet account ids | Ids and amounts that were requested |
| `postings.core_ref` | the core's reference | An opaque string |
| `breaks.account_ref` | wallet account id | The id, the amount, the reference number |

An amount copied into a projection is a snapshot with a timestamp, not a fact. If it
is read as truth, the projection has become a second source and must be deleted.

## Migration rules

- One migration directory per service, owned by that service's team.
- Forward-only in production. A down migration exists when it is safe; it is never the
  recovery plan.
- A migration that rewrites a large table runs in a maintenance window with the
  service degraded, not with `ALTER TABLE` blocking writes indefinitely.
- Adding a non-null column to a large table: add nullable, backfill in batches, then
  add the constraint — three migrations, not one.
- Migrations are reviewed for lock duration as well as correctness.

## Sizes and growth

Estimates for the pilot and the first year, used to size indexes and retention:

| Table | Rows at pilot | Rows after 12 months | Growth driver |
|---|---|---|---|
| `entries` | ~2M | ~60M | 2–4 per money operation |
| `transactions` | ~0.7M | ~20M | 1 per operation |
| `holds` | ~200k | ~6M | 1 per outbound or review |
| `outbox` | ~1M (pruned) | ~10M (pruned) | 1–3 per operation |
| `deliveries` | ~500k | ~10M | 1 per event per subscription |

At these sizes every index above is comfortable, and derivation with checkpoints stays
in tens of milliseconds. The numbers exist so that a decision to partition is made
with a date attached rather than in an incident.
