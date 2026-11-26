# Local stack

PostgreSQL 17, NATS with JetStream, and Redis. The services run from source;
images arrive with the deployment stage.

## Ports

| Component | Port | Notes |
|---|---|---|
| PostgreSQL | 5432 | One instance, one database per service |
| NATS | 4222 | JetStream enabled; monitoring on 8222 |
| Redis | 6379 | Rate limits and short-lived state only |
| Wallet internal API | 8182 | Served by the wallet process, not by compose; `X-Echo-Service-Token` required (`make up` defaults it to `local-dev-service-token`) |

Service ports are in `docs/reference/environments.md`.

## Databases and roles

`postgres-init/01-databases.sql` creates:

| Database | Role |
|---|---|
| `echo_auth` | `echo_auth_app` |
| `echo_wallet` | `echo_wallet_app` |
| `echo_transfer` | `echo_transfer_app` |
| `echo_notification` | `echo_notification_app` |
| `echo_webhook` | `echo_webhook_app` |
| `echo_recon` | `echo_recon_app` |

Each role owns its own database and has no grant on another. The passwords in
the init script are local development credentials and must never be reused
outside this stack.

## Commands

```bash
make up       # start the stack, build, migrate and run all seven services
make logs     # follow the services' logs
make down     # stop the services and the stack, keeping data
make reset    # stop everything, delete the volumes, start again
```

The data volume `pgdata` survives `make down`. `make reset` deletes it, which
is the only way to re-run the init script: it runs once, on an empty volume.
