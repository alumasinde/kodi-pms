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
    (echo "SET @demo_seed_confirmed = 'yes';"; cat db/demo/demo_seed.sql) \
      | mysql -u user -p rental

Demo login: `demo@kodipms.co.ke` / `Demo@2026!`, on the `localhost` domain.
Quick PIN for the demo admin: `2026` (password login on the device first).
Apply it only to a dedicated demo database; it uses ids in the 9000 range, and
the seed aborts unless `@demo_seed_confirmed` is set. The seed is written for
the 000013-000018 schema (per-org columns, M-Pesa routing, no plaintext PII) and
should be applied to a freshly migrated database.

## Running the API
    cp .env.example .env        # set DATABASE_URL, a real JWT_SECRET and PII_ENCRYPTION_KEY
    go run ./cmd/api

Health: `GET /healthz` and `GET /readyz` (database ping).
Redis is optional in development; without it the API uses an in-process cache.
Outside development `REDIS_ADDR` is required, and a configured-but-unreachable
Redis is a startup error: lockouts and token revocation must be shared by every
instance. Password-reset and invite delivery also require `SMTP_URL` and
`MAIL_FROM` outside development; the startup check fails without them.
`PII_ENCRYPTION_KEY` must be base64 for 32 bytes (`openssl rand -base64 32`)
outside development; development falls back to a published key. Changing the
key makes existing encrypted PII unreadable. Expired refresh and one-time
tokens are purged hourly by the API process.

### Auth endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/v1/auth/login` | public | identifier/email/phone + password; host must resolve to the org domain |
| `POST /api/v1/auth/pin-login` | public | identifier + 4-6 digit PIN; requires the refresh token of a device that already completed a password login |
| `POST /api/v1/auth/refresh` | refresh token | cookie or JSON body; rotates the token, detects reuse and allows one 30-second retry |
| `POST /api/v1/auth/forgot-password` | public | always 200; queued lookup and email delivery (SMTP), so timing does not reveal accounts |
| `POST /api/v1/auth/reset-password` | token | one-time token, bumps the user token version, deletes all refresh tokens and clears the PIN |
| `POST /api/v1/auth/accept-invite` | token | one-time invite token, activates the user and clears any PIN |
| `POST /api/v1/auth/invites` | access + `users.create` | invites or re-invites a user, assigns a role and delivers the link by SMTP |
| `GET /api/v1/auth/me` | access | user, org, roles, permissions, derived data scope, `has_pin` |
| `POST /api/v1/auth/logout` | access | revokes the refresh family and deny-lists the access token |
| `POST /api/v1/auth/change-password` | access | bumps the user token version and revokes refresh tokens |
| `POST /api/v1/auth/pin` | access | set or replace the quick PIN; requires `current_password` |

Access tokens are 15-minute JWTs sent as `Authorization: Bearer`; they carry a
per-user `ver` claim that is compared with `users.token_version`, so a password
reset or change invalidates existing access tokens immediately without flushing
the whole organization's authorization cache. Refresh tokens are 30-day rotating
tokens stored only as SHA-256 hashes; the web app receives an HttpOnly cookie,
mobile clients get the value in the JSON body.

A PIN is optional, hashed with bcrypt alongside the password, and is only for
quick sign-in on a device that already holds a live refresh token from a password
login. It is 4-6 digits, is blocked when `must_change_password` is set, and both
password and PIN failures increment the same DB-backed account lock (5 failures,
15 minutes); the Redis IP+identifier counter is a second layer, not the authority.

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
| 12 | security_hardening             | users.token_version, DB login lock fields, refresh rotation/grace fields |
| 13 | tenancy_and_lease_integrity    | per-org user uniqueness, units.organization_id, composite org FKs, one active lease per unit, billing day checks |
| 14 | billing_ledger                 | ledger_accounts, ledger_entries, credit_notes, tenant_credit_entries, payment_reversals, allocation triggers, derived invoices.amount_paid |
| 15 | owner_money                    | owner_statements, owner_statement_lines, owner_commissions, owner_disbursements |
| 16 | mpesa_routing                  | mpesa_shortcodes, mpesa_account_refs, shortcode/lease links on M-Pesa rows |
| 17 | pii_and_retention              | encrypted national ID/KRA PIN/income/payout columns, national_id_hash, pii_retention_policies, pii_access_logs |
| 18 | datetime_housekeeping          | all TIMESTAMP columns become DATETIME (2038-safe) |

## Rules that keep this safe
1. **Never edit a migration that has run anywhere.** Add a new numbered pair instead (e.g. `000019_add_x.up.sql`).
2. Table `up`s use `CREATE TABLE IF NOT EXISTS`, table `down`s use `DROP TABLE IF EXISTS`, and seeds use `INSERT IGNORE`, so a half-finished run can be retried. `ALTER TABLE` migrations (e.g. 000013+) are the exception: MySQL 8 has no `ADD COLUMN IF NOT EXISTS`, so retrying a partially-applied one needs `force` after fixing the schema by hand.
3. MySQL DDL auto-commits (no transactional rollback). If a migration fails midway the DB is marked *dirty*: fix the schema, then `go run ./migrate force <last good version>` and re-run.
4. `down` drops tables in strict reverse-dependency order, so no `FOREIGN_KEY_CHECKS=0` is needed. Seed `down`s only delete rows nothing references.
5. `down` defaults to ONE step. `down -all` needs `-yes`; with `APP_ENV=production`, `down`, `goto` and `force` all need `-yes`.
6. Rolling back a table migration deletes that table's data. Back up first in production. **000017 drops the old plaintext PII columns**: back up before applying it if those values cannot be re-entered.
7. golang-migrate takes a MySQL advisory lock, so two app instances running `up` at once won't collide.
8. `audit_logs`, `ledger_entries`, `tenant_credit_entries` and `pii_access_logs` are append-only (triggers reject UPDATE/DELETE). Grant the app DB user only INSERT/SELECT on them, and use a separate migration user for DDL.
9. 000013+ require MySQL 8.0.16+ (CHECK constraints) and generated columns. Rolling 000013 back fails if the data now has duplicate emails/phones per org or two active leases on one unit; clean those rows first.

## Design notes vs. the files you shared
- Added `organization_id` (multi-tenancy) to top-level tables; child tables inherit it via their parent.
- `rent_payments` is replaced by `payments` + `payment_allocations` (one payment can settle several invoices); `late_fees` now hangs off `invoices`.
- `inspection_items.condition` renamed `condition_rating` (`condition` is reserved in MySQL).
- `leases.rent/deposit` became `rent_amount/deposit_amount`.
- Kenya specifics: KES default, counties/estates, KRA PIN, national ID, E.164 phones, EARB licence, M-Pesa C2B/STK tables, DSQ/bedsitter lookups.
- `invoices` is unique on `(lease_id, invoice_type, billing_period_start)`, so a monthly billing job can safely re-run.
- Status columns are VARCHAR + CHECK (no ENUM); CHECKs need MySQL 8.0.16+.

## What migrations 000013-000018 enforce
- **Identity is per organization.** `users` is unique on `(organization_id, email)` and
  `(organization_id, phone)` while the user is live; soft-deleting releases both.
  Login, PIN login, password reset and invites all look users up inside the resolved
  host organization (platform admins with `organization_id = NULL` still match).
- **No cross-org references.** Units carry `organization_id`, and leases, invoices,
  payments, allocations, deposits, documents, insurance and reviews use composite
  `(organization_id, id)` foreign keys; messages carry `organization_id` and a
  trigger validates both participants. A unit can have one active lease
  (`leases.active_unit_id` generated column + unique key); leaving `active` releases it.
- **Billing invariants live in the database.** `invoices.amount_paid` is derived from
  live allocations by triggers; an allocation cannot exceed the payment or the invoice
  balance; allocations are immutable; payments cannot be deleted or shrink below their
  allocations; a reversal is a full-payment `payment_reversals` row. `ledger_entries`,
  `tenant_credit_entries` and `pii_access_logs` are append-only. The billing service
  still has to *post* balanced ledger rows for each money event — the triggers enforce
  consistency, not double-entry completeness.
- **Owner money is explicit.** `owner_statements` + lines, `owner_commissions` and
  `owner_disbursements` record what each owner is owed and paid; `owners.payout_*`
  columns are encrypted.
- **M-Pesa is routed, not guessed.** `mpesa_shortcodes` holds per-org, per-environment
  credentials (encrypted) and the callback token hash; `mpesa_account_refs` maps
  `BillRefNumber` to a lease. Transactions and STK requests carry both links.
- **PII is encrypted.** National ID, KRA PIN, tenant income and owner payout details
  live in `*_enc` VARBINARY columns sealed with AES-256-GCM by
  `internal/platform/pii`; `national_id_hash` (keyed HMAC-SHA256) allows duplicate
  checks and `national_id_last4` allows masked display. Reads that reveal these fields must call
  `pii.AccessLog.Record`, which writes the append-only `pii_access_logs` row for
  `tenants.view_sensitive`. `pii_retention_policies` and `pii_retention_until` schedule
  anonymisation; `pii_anonymized_at` records when it happened. A key rotation requires
  decrypt-with-old/encrypt-with-new and recomputing `national_id_hash`; the ciphertext
  carries a version byte for that.
- **Dates are 2038-safe and tokens are purged.** All application timestamps are DATETIME
  in UTC; the API deletes expired refresh tokens and used/expired one-time tokens hourly.
