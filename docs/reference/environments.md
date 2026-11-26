# Environments

Five environments, each with one job. The rules that follow exist so that "works on my
machine" is not a category of problem this project has.

| Environment | Job | Data | Who can reach it |
|---|---|---|---|
| Local | Build and test on one laptop | Synthetic | The developer |
| CI | Prove a change is mergeable | Synthetic, ephemeral | Nobody |
| Staging | Prove the system works together | Synthetic | The team |
| Pilot | Prove it works with real money and a few customers | Real, small | The team and the institution |
| Production | Run it | Real | Operations |

The gap between each pair is deliberate and documented. A change that has never been
seen in the environment above it is not ready for the environment above that.

---

## Local

### One command

```
make up      # Postgres, NATS, Redis, build, migrate, seven services
make logs    # follow the services
make down    # stop the services and the stack, keeping data
make reset   # destroy volumes, re-create, migrate, start
```

`make up` produces a working platform on `localhost`:

| Service | HTTP | Metrics |
|---|---|---|
| gateway | 8080 (public) | 9080 |
| auth | 8081 | 9081 |
| wallet | 8082 | 9082 |
| transfer | 8083 | 9083 |
| notification | 8084 | 9084 |
| webhook | 8085 | 9085 |
| recon | 8086 | 9086 |

The wallet also serves its internal API on `:8182`, behind `WALLET_SERVICE_TOKEN`.
`make up` defaults that token to `local-dev-service-token`; it has no default in the
binary, and the port is never published in production.

Only the gateway's port is public. Postgres is on `:5432`, NATS on `:4222`
(monitoring `:8222`), Redis on `:6379`. The service ports are for local
inspection; production exposes the gateway alone.

From S4, when the endpoint exists, a developer who has cloned the repository and
run `make up` should be able to call `POST /v1/auth/register` before they have read
any code. If they cannot, the local stack is broken and fixing it is more valuable
than the feature they were about to write.

### The stack

| Component | Image / version | Notes |
|---|---|---|
| Go | 1.26 | The toolchain the module declares |
| PostgreSQL | 17 | One instance, one database per service, separate roles |
| NATS | 2.x with JetStream | Streams created by `deploy/nats/` on start |
| Redis | 7.x | Rate limits, session denylist, projections only |

Every service gets its own database and its own role:

```
echo_auth         echo_auth_app
echo_wallet       echo_wallet_app
echo_transfer     echo_transfer_app
echo_notification echo_notification_app
echo_webhook      echo_webhook_app
echo_recon        echo_recon_app
```

`echo_wallet_app` cannot `SELECT` from `echo_auth`. The local stack enforces the
production boundary, because a boundary that only exists in production is a boundary
that gets broken locally and discovered later.

The wallet role has `INSERT` and `SELECT` on `entries` and no `UPDATE` or `DELETE`. This
is how Article IV is enforced at the database rather than by convention, and it is
enforced locally too.

### Fixtures

`make seed` creates:

- Three users with known passwords (documented in `deploy/seed/README.md`)
- One wallet each, funded
- One operator user with operator scopes
- One partner with a webhook subscription pointing at a local receiver
- A mock core and a mock rail, both controllable (see below)

The mocks are not stubs that always say yes. Each can be set to apply, reject, time out
after applying, time out before applying, and hang — because those are the cases that
break systems, and a developer should be able to reproduce them in one command.

```
make mock-core MODE=timeout-after-apply
make mock-rail MODE=hang
```

---

## CI

Ephemeral. Every job starts from a clean checkout.

| Job | Runs | Gate |
|---|---|---|
| `fmt` | `gofmt -l`, `goimports` | merge-blocking |
| `vet` | `go vet`, `staticcheck` | merge-blocking |
| `lint` | golangci-lint, including the deadline-to-unknown rule | merge-blocking |
| `unit` | `make test` with `-race` | merge-blocking |
| `integration` | services from a compose file, real Postgres/NATS | merge-blocking |
| `contract` | schema and event contracts | merge-blocking |
| `generate` | `sqlc generate` + `go generate`, asserts no diff | merge-blocking |
| `migrate` | apply from empty and from the previous release | merge-blocking |
| `boundaries` | import graph rules (no cross-service imports, domain is pure) | merge-blocking |
| `security` | `govulncheck`, image scan, secret scan | merge-blocking on critical/high |
| `e2e` | full stack journey | nightly |
| `load` | steady profile | nightly |
| `chaos` | four scenarios | weekly |

A red gate stops the merge. A flaky gate is fixed in the week it appears; it is not
retried into greenness.

### Caching and time

Target: a pull request's full CI in under 15 minutes. If it grows beyond that, the
conversation is about which job to split, not about turning jobs off.

---

## Staging

The whole platform, together, in an environment that looks like production but with no
real money.

| Concern | Staging |
|---|---|
| Infra | Same manifests as production, smaller |
| Data | Synthetic, generated by the load tool; reset weekly |
| Core | Apache Fineract or the local CBA, whichever production will use |
| Rail | NIP simulator (S8) |
| Access | VPN, team only |
| Deploys | Automatic on merge to `main` |

Staging is where the integration problems live: the core's version, the rail's sandbox,
the gateway's routing under TLS. Production should be the first place an integration
change is *stable*, not the first place it is *exercised*.

### Synthetic monitoring

A read-only and a write path are exercised on a schedule against synthetic customers:

- Register → fund → read balance → withdraw (small amounts, synthetic users only)
- One transfer → wait for the webhook → assert delivery

Failure pages the on-call. This catches the "everything is green but nobody can log in"
failure that unit tests and dashboards both miss.

---

## Pilot

Real money, a few real customers, a controlled institution.

| Concern | Pilot |
|---|---|
| Customers | A small, agreed cohort |
| Amounts | Capped per transaction and per day |
| Core | The institution's actual core, the version recorded below |
| Rail | The operator's sandbox or a limited live window, per the operator's rules |
| Reconciliation | Daily, with a human reviewing every break |
| Rollback | Documented, rehearsed, and possible for each service |
| Cutover | Business hours only, with the team present |

Two artifacts gate the pilot:

1. **The shadow day.** The platform runs alongside the existing process for a full
   business day on real inputs, and the two reconcile exactly before any customer
   transacts.
2. **The go/no-go.** Written before day one, with numbers in it: how many operations,
   what error rate, what divergence, what incident count. Written afterwards, it is not
   a criterion, it is a rationalisation.

The daily ritual in S9 is the operating model here: a fifteen-minute review of the
reconciliation runs, the open breaks, the unknown outcomes, and the SLOs.

---

## Production

| Concern | Rule |
|---|---|
| Deploys | Blue/green or rolling with a health gate; no deploy during cut-off or settlement |
| Migrations | Applied before the deploy, backward-compatible with the running version |
| Feature flags | New behaviour is behind a flag; removing a flag is part of done |
| Rollback | The previous image digest is known and tested |
| Access | Break-glass only, time-boxed and audited |
| Backups | Tested by restore, not by existence |
| Runbooks | Linked from every alert |

### Change windows

- No deploys within 30 minutes of a reconciliation cut-off or a settlement cycle.
- No schema migration that locks a ledger table during business hours.
- A deploy that changes money behaviour ships behind a flag and is enabled deliberately.

---

## Version records

The definitions of done in S7 and S8 require the exact versions to be recorded, because
"our core is *a* Fineract" is not a fact you can reproduce an incident against.

### Core banking

| Environment | Implementation | Version | Recorded |
|---|---|---|---|
| Local | Local CBA (NestJS) / mock | TBD at S7 | — |
| CI | mock | — | — |
| Staging | TBD at S7 | — | — |
| Pilot | TBD at S7 | — | — |
| Production | TBD at S7 | — | — |

Filled in when the S7 posting adapter is written, and updated whenever the core is
upgraded. The large-history behaviour discussed in the ecosystem documents — derived
balances that slow down as history grows, and the bloat in the core's broader modules —
is an input to the S7 design: **the platform never asks the core for a balance on the
hot path** (Article X). The core is asked to post, and to confirm by reference. If the
core is slow at a particular operation, that is an integration fact recorded here, not a
reason to move the source of truth into the payments platform.

### Rail

| Environment | Implementation | Version | Recorded |
|---|---|---|---|
| Local | NIP simulator | TBD at S8 | — |
| CI | NIP simulator | TBD at S8 | — |
| Staging | NIP simulator | TBD at S8 | — |
| Pilot | Operator sandbox | TBD at S8 | — |
| Production | Operator production | TBD at S8 | — |

The rail's code mapping table (`internal/rail/nip/codes.go`) is versioned with the
record: an operator that changes its response codes changes the mapping, and the change
is reviewed against the operator's specification.

### Platform

Recorded per release, in `deploy/releases/<version>.yaml`: image digests, migration
versions applied, config version, core and rail versions from the tables above, and the
synthetic monitoring state at the time.

---

## Configuration

Configuration comes from the environment and is validated at startup. A missing or
invalid value stops the process with a message naming the key. There is no default for
anything that could be wrong in production.

| Kind | Source |
|---|---|
| Secrets | Secret manager, mounted or injected |
| Non-secret config | Environment or a config map |
| Feature flags | Flag service, evaluated at the edge |
| Tunables (timeouts, limits) | Config, with a sane default and a documented bound |

A timeout is configuration, not a constant, because the core's latency and the rail's
latency are facts about other people's systems and they change.

---

## Local versus production: what is allowed to differ

| May differ | Must not differ |
|---|---|
| Scale | Schema and constraints |
| Core and rail implementations | The three-state outcome model |
| Latency | Invariants and their enforcement |
| Provider (email, SMS) | The event envelope and contracts |
| Observability backend | The metrics and spans emitted |
| Data | Authorisation boundaries |
| — | The ledger's derivation and checkpoint rules |

If something in the second column differs between environments, it is a bug in the
environment, and it will be found in production during an incident at the worst moment.
