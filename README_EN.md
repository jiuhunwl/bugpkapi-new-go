# BugPk-Api Backend

[简体中文](README.md) · [English](README_EN.md)

Open-source backend for an **API-as-a-Service platform**, built in Go with [Hertz](https://github.com/cloudwego/hertz) and a DDD layout. It powers the admin API, the open API gateway, APIs and groups, billing and plans, orders and payments, risk control and audit, tickets, and content operations.

| Repository | Description |
| --- | --- |
| **bugpkapi-new-go** (this repo) | Backend service (Go + MySQL + Redis), AGPL-3.0 |
| [bugpkapi-new-vue](https://github.com/jiuhunwl/bugpkapi-new-vue) | Frontend (Vue 3 + Vben Admin), MIT |
| [bugpkapi-new-docs](https://github.com/jiuhunwl/bugpkapi-new-docs) | Documentation, architecture and deployment guides |

## Features

| Module | Capability |
| --- | --- |
| Open API gateway | Single `/api/{apiCode}` entry, upstream forwarding per API config, validation, caching and rate limiting |
| APIs and billing | API management, QPS limits, plans (site-wide / per-API / credits), per-call billing, balances and ledgers |
| Payments | Alipay, WeChat Pay and pluggable channels; idempotent callbacks, reconciliation, replay protection |
| Users and permissions | RBAC roles / menus / buttons, sign-up and password reset, multi-provider captcha |
| Risk control | Call-log fingerprinting, risk snapshots, account bans, IP geolocation |
| Content operations | Site settings, SEO, announcements, ad slots, friend links, donations and acknowledgements, agreement templates |
| Analytics | Incremental call-log rollups (minute / hour / day) decoupled from raw logs |
| Tickets | User tickets and messages, admin workflow, attachment policy |
| Observability | Unified request ID, operation logs, `/healthz` and `/readyz` |

## Layout

```text
.
├── cmd/            executable entry points (main service and ops tools)
├── internal/       DDD layers (app / domain / infra / pkg)
├── migrations/     database migrations (source of truth)
├── sql/full/       full init SQL generated from migrations
├── configs/        configuration examples
├── deploy/         generic deployment templates (systemd / Nginx / env examples)
└── scripts/        engineering scripts (full SQL generation, etc.)
```

## Requirements

- Go `1.26+`
- MySQL `8.0+`
- Redis `6+` (cache, rate limiting, captcha)
- Linux / macOS / Windows for local development

## Quick start

```bash
# 1. Configuration
cp configs/config.yaml.example configs/config.yaml

# 2. Create an empty database (never reuse a database holding data)
mysql -uroot -p -e "CREATE DATABASE bugpk_api DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 3. Import the init SQL (it aborts on a non-empty database)
mysql -uroot -p bugpk_api < sql/full/bugpk_api_full_v1.2.113.sql

# 4. Set the first administrator password and start
export INITIAL_ADMIN_PASSWORD='<8-64 char strong password>'
go run ./cmd/bugpk-api
```

If no administrator exists, the first start creates `admin` with `INITIAL_ADMIN_PASSWORD`; you can remove the variable afterwards. In production inject the database password, JWT signing key and payment config secret through environment variables — never commit them or hardcode them in `config.yaml`.

See [deploy](./deploy) and the docs repository for the remaining variables (`APP_ENV`, `DB_*`, `JWT_SECRET`, `PAYMENT_CONFIG_SECRET`, `FRONTEND_SERVE_ENABLED`, …) and deployment guides.

## Build

```bash
make build              # current platform
make build-linux-amd64  # linux/amd64
make build-linux-arm64  # linux/arm64
```

## Database and migrations

`migrations/*.up.sql` is the only source of truth; `sql/full` is generated from it and kept in sync by a contract test:

```bash
pwsh ./scripts/generate-full-sql.ps1 -Variant production
go test ./internal/app -run TestSQLArtifactsMatchCurrentMigrations -count=1
```

- The full SQL contains **no user seeds**; the administrator is created from `INITIAL_ADMIN_PASSWORD`.
- The full SQL **never creates or switches databases** and requires an empty target database.
- After importing the full SQL manually, set `MIGRATION_ENABLED` to `false` to avoid re-running migrations.

## Tests

```bash
make test            # go test -count=1 ./...
make vet
make release-check   # build + vet + test
```

Some integration tests need MySQL / Redis and skip automatically when the environment is not configured.

## Frontend integration

The frontend proxies `/system-api` and `/api` to `http://127.0.0.1:8080` by default. This service can also serve the built frontend:

```yaml
frontend:
  enabled: true
  distPath: frontend/dist
```

## License

Licensed under the [GNU Affero General Public License v3.0](./LICENSE) (AGPL-3.0).

AGPL section 13 requires that if you modify this program and make it available over a network, you must offer the corresponding source to those users. The companion frontend is MIT-licensed; see its LICENSE file.