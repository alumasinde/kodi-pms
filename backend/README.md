# Rental / Property Management - Backend (Go, MySQL 8.0.16+, golang-migrate)

## Layout
    cmd/api/main.go            # process startup and dependency wiring only
    internal/routes/api.go     # global router, middleware and module registration
    internal/<module>/         # handler, service, repository, routes.go, model.go, dto.go
    internal/                  # platform, tenancy, auth, rbac, modules
    db/migrations/NNNNNN_name.up.sql | .down.sql
    migrate/main.go

## Setup
    go mod init yourmodule        # skip if you already have go.mod
    go get github.com/golang-migrate/migrate/v4
    go get github.com/golang-migrate/migrate/v4/database/mysql
    go mod tidy
    export DATABASE_URL="mysql://user:pass@tcp(127.0.0.1:3306)/rental"   # database must already exist

    go run ./migrate up          # apply everything pending (safe to run any time)
    go run ./migrate down        # roll back one migration
    go run ./migrate version

## Demo data
    mysql -u user -p rental < db/demo/demo_seed.sql

Demo login: `demo@kodipms.co.ke` / `Demo@2026!`, on the `localhost` domain.
Quick PIN for the demo admin: `2026`.
Apply it only to a dedicated demo database; it uses ids in the 9000 range.

## Running the API
    cp .env.example .env        # set DATABASE_URL and a real JWT_SECRET
    go run ./cmd/api

Health: `GET /healthz` and `GET /readyz` (database ping).
Redis is optional in development; without it the API uses an in-process cache.
In production a configured-but-unreachable Redis is a startup error.

### Auth endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/v1/auth/login` | public | identifier/email/phone + password; host must resolve to the org domain |
| `POST /api/v1/auth/pin-login` | public | identifier/email/phone + 4-6 digit PIN; same lockout pattern as password login |
| `POST /api/v1/auth/refresh` | refresh token | cookie or JSON body; rotates the token and detects reuse |
| `POST /api/v1/auth/forgot-password` | public | always 200; reset link is logged in development only |
| `POST /api/v1/auth/reset-password` | token | one-time token, invalidates all refresh tokens and clears the PIN |
| `POST /api/v1/auth/accept-invite` | token | one-time invite token, activates the user and clears any PIN |
| `GET /api/v1/auth/me` | access | user, org, roles, permissions, derived data scope, `has_pin` |
| `POST /api/v1/auth/logout` | access | revokes the refresh family and deny-lists the access token |
| `POST /api/v1/auth/change-password` | access | revokes other refresh tokens |
| `POST /api/v1/auth/pin` | access | set or replace the quick PIN; requires `current_password` |

Access tokens are 15-minute JWTs sent as `Authorization: Bearer`. Refresh tokens
are 30-day rotating tokens stored only as SHA-256 hashes; the web app receives an
HttpOnly cookie, mobile clients get the value in the JSON body.

A PIN is optional, hashed with bcrypt alongside the password, and is only for
quick sign-in: it is 4-6 digits, is blocked when `must_change_password` is set,
and uses a separate 5-failures/15-minutes throttle keyed by IP and identifier.

## Migration order
| #  | File                           | Tables |
|----|--------------------------------|--------|
| 01 | core_administration            | organizations, organization_domains, system_settings |
| 02 | identity_access                | users, roles, permissions, role_permissions, user_roles, refresh_tokens, user_tokens, owners, agents, tenants |
| 03 | seed_roles_and_settings        | seed: roles, permissions, default role matrix, default settings (KES, late fees...) |
| 04 | property_and_asset_management  | property_types, unit_types, amenities, properties, units, property_amenities, unit_amenities, parking_spots, property_agents, property_assignments |
| 05 | lease_and_tenant_placement     | leases, lease_tenants, deposits, deposit_refunds, documents, insurance_policies |
| 06 | billing_ledger_and_payments    | payment_methods, invoices, invoice_items, payments, payment_allocations, late_fees |
| 07 | mpesa_integration              | mpesa_transactions, mpesa_stk_requests |
| 08 | utilities_and_consumption      | utility_meters, utility_readings, utility_bills |
| 09 | maintenance_and_inspections    | contractors, maintenance_requests, maintenance_jobs, maintenance_expenses, maintenance_logs, inspections, inspection_items |
| 10 | communications_and_audit       | notifications, messages, reviews, audit_logs |
| 11 | seed_reference_data            | seed: property/unit types, amenities, payment methods |

## Rules that keep this safe
1. **Never edit a migration that has run anywhere.** Add a new numbered pair instead (e.g. `000012_add_x.up.sql`).
2. Every `up` uses `CREATE TABLE IF NOT EXISTS`, every `down` uses `DROP TABLE IF EXISTS`, and seeds use `INSERT IGNORE`, so a half-finished run can be retried.
3. MySQL DDL auto-commits (no transactional rollback). If a migration fails midway the DB is marked *dirty*: fix the schema, then `go run ./migrate force <last good version>` and re-run.
4. `down` drops tables in strict reverse-dependency order, so no `FOREIGN_KEY_CHECKS=0` is needed. Seed `down`s only delete rows nothing references.
5. `down` defaults to ONE step. `down -all` needs `-yes`; with `APP_ENV=production` every `down` needs `-yes`.
6. Rolling back a table migration deletes that table's data. Back up first in production.
7. golang-migrate takes a MySQL advisory lock, so two app instances running `up` at once won't collide.
8. For `audit_logs`, grant the app DB user only INSERT/SELECT.

## Design notes vs. the files you shared
- Added `organization_id` (multi-tenancy) to top-level tables; child tables inherit it via their parent.
- `rent_payments` is replaced by `payments` + `payment_allocations` (one payment can settle several invoices); `late_fees` now hangs off `invoices`.
- `inspection_items.condition` renamed `condition_rating` (`condition` is reserved in MySQL).
- `leases.rent/deposit` became `rent_amount/deposit_amount`.
- Kenya specifics: KES default, counties/estates, KRA PIN, national ID, E.164 phones, EARB licence, M-Pesa C2B/STK tables, DSQ/bedsitter lookups.
- `invoices` is unique on `(lease_id, invoice_type, billing_period_start)`, so a monthly billing job can safely re-run.
- Status columns are VARCHAR + CHECK (no ENUM); CHECKs need MySQL 8.0.16+.
