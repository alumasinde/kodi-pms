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
the consolidated 000001-000011 schema (per-org columns, ledger and owner money,
M-Pesa routing, no plaintext PII) and should be applied to a freshly migrated
database.

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
tokens are purged hourly by the API process, and so are leases whose end date
has passed (they become `expired`; the unit stays occupied until it is
terminated or renewed).

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

### Settings endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/settings` | access + `settings.view` | organization profile plus the effective settings (global defaults merged with this organization's overrides) |
| `PATCH /api/v1/settings` | access + `settings.update` | partial `organization` fields and/or a `settings` map; no-op fields are ignored |

`settings` returns every global key except `currency_code` (that column belongs
to the organization profile, so update it through `organization`). Each item
carries `value`, `value_type`, `overridden` and `default_value`, so the UI can
show which values are customised. Setting values are type-checked and
range-checked before writing; unknown keys are rejected. Sending `null` for a setting removes
the organization override and reverts to the global default. In `organization`,
omit a field to leave it unchanged and send an empty string to clear an optional
field. The effective list is cached for 10 minutes under `settings:org:{orgID}`
and invalidated on write; an organization profile change also invalidates the
`domain:{host}` resolution cache.

### Properties endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/properties` | access + `properties.view` | paginated list narrowed to the caller's property scope; filters `q` (name/code/address/estate/town), `status`, `type_id`, `owner_id`, `county`; `sort` accepts name, code, status, county, created_at, updated_at, units_total with a `-` prefix for descending |
| `GET /api/v1/properties/lookups` | access + `properties.view` | property types and amenities for form dropdowns |
| `GET /api/v1/properties/{id}` | access + `properties.view` | property with owner, type, unit counts and amenities |
| `POST /api/v1/properties` | access + `properties.create` | creates a property and its amenities |
| `PATCH /api/v1/properties/{id}` | access + `properties.update` | partial update; the owner is immutable |
| `DELETE /api/v1/properties/{id}` | access + `properties.delete` | soft delete, refused while non-deleted units exist |

Properties are looked up inside the resolved organization and the caller's scope;
an id outside either is a 404, not a 403. `code` is optional but unique per
organization, including soft-deleted properties, so a code stays reserved after
deletion. `owner_id` and `type_id` must exist. Amenities are replaced wholesale
when `amenity_ids` is sent (send `[]` to clear, omit it to keep them). On
`PATCH`, omit a field to leave it unchanged, send `""` to clear an optional
string, and send `null` to clear `latitude`, `longitude` or `year_built`.
Latitude and longitude must end up either both set or both cleared. Lookup lists are
cached for 1 hour under `lookup:property_types` and `lookup:amenities`. Writes
are audited as `properties.created`, `properties.updated` and
`properties.deleted`.

### Users endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/users` | access + `users.view` | paginated staff directory for the resolved organization; filters `q` (name/email/phone), `status`, `role_id`; `sort` accepts first_name, last_name, email, status, last_login_at, created_at, updated_at with a `-` prefix for descending |
| `GET /api/v1/users/lookups` | access + `users.view` | roles of this organization for the role picker |
| `GET /api/v1/users/{id}` | access + `users.view` | user detail, including `national_id_last4` and verification timestamps |
| `GET /api/v1/users/{id}/assignments` | access + `users.view` | property assignments (manager / caretaker) for that user |
| `POST /api/v1/users` | access + `users.create` | creates a user with roles and an optional national ID |
| `PATCH /api/v1/users/{id}` | access + `users.update` | partial update; roles, password, status and national ID are all patchable |
| `PUT /api/v1/users/{id}/assignments` | access + `users.update` | replaces the whole assignment list (`[]` clears it) |
| `DELETE /api/v1/users/{id}` | access + `users.delete` | soft delete plus session revoke; never your own account |

`role_ids` is required and every role must belong to this organization.
`org_admin` and `system_admin` can only be granted by a caller who already holds
that role, so nobody escalates past their own rank. Email is validated with
`net/mail` and lowercased, phone must be E.164 (`+` then 7-19 digits), and the
password goes through the same `auth.ValidatePassword` rules as sign-up (8-72
characters, not blank, not on the common-password list). `must_change_password`
defaults to `true` so an admin-set password is forced to change on first login.
`national_id` is encrypted with AES-256-GCM, hashed (keyed HMAC) for duplicate
detection and stored with only the last 4 digits readable; the module never
selects the ciphertext or the hash, and `""` / `null` in a `PATCH` clears it.
A duplicate live email or phone inside the organization is a 409; a national ID
that is already on another user is rejected on `national_id`.
On your own account `role_ids`, `password` and `status` are rejected — change a
password through the auth flow, not here. Setting a password or moving a user
off `active` revokes their refresh tokens, marks issued tokens used, and (for a
status change) bumps `users.token_version` so an already-issued access token
dies at the next request. An `org_admin` cannot be suspended or demoted, and
cannot be deleted, while they are the organization's only active administrator
(409). Any role or assignment change bumps the org authorization cache version,
so the next request reloads permissions for everyone affected.
Roles are cached for 1 hour under `lookup:user_roles:{orgID}`. Writes are
audited as `users.created`, `users.updated`, `users.deleted` and
`users.assignments_updated`; audit payloads record only `national_id_last4`.

### Units endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/units` | access + `units.view` | paginated list narrowed to the caller's property scope; filters `q` (unit number, property name/code), `status`, `property_id`, `type_id`; `sort` accepts number, rent, floor, bedrooms, status, created_at, updated_at, property with a `-` prefix for descending |
| `GET /api/v1/units/lookups` | access + `units.view` | unit types and amenities for form dropdowns |
| `GET /api/v1/units/{id}` | access + `units.view` | unit with amenities and, when leased, the current occupant |
| `POST /api/v1/units` | access + `units.create` | creates one unit in a property |
| `POST /api/v1/units/generate` | access + `units.create` | creates a whole block of sequentially numbered units sharing one template |
| `PATCH /api/v1/units/{id}` | access + `units.update` | partial update; `property_id` is immutable |
| `DELETE /api/v1/units/{id}` | access + `units.delete` | soft delete, refused while an active lease exists |

`property_id` and `type_id` must reference a property inside the caller's scope
and a real unit type; an out-of-scope property reads as a validation error
rather than leaking the property. `number` is unique per property including
soft-deleted rows, matching `uq_units_property_number`, so a cleared door keeps
its number reserved. `status` is one of `vacant`, `occupied`, `reserved`,
`maintenance`, `inactive`, and `occupied` can never be set by hand — the lease
module owns it. On `PATCH`, a unit with an active lease cannot be moved away
from `occupied` or deleted. `floor` accepts `-1` (basement) down to `-10` and up
to `200`; `bedrooms` 0-50; `bathrooms` is rounded to one decimal, money fields
to cents. Omit a field to leave it unchanged and send `null` to clear `floor` or
`square_feet`; `number` and `status` are required, so an empty string is a
validation error rather than a clear.
`amenity_ids` replaces the unit's amenities when sent (`[]` clears).
`generate` builds numbers as `prefix` + zero-padded counter starting at
`first_number` (default 1) for `count` units (1-200); `pad` is 0-5, the prefix
is at most 10 characters and cannot contain the SQL wildcards `%` or `_`. If any
number in the run already exists, the request 409s with the offending list and
nothing is created. Lookup lists are cached for 1 hour under
`lookup:unit_types` and `lookup:amenities`. Writes are audited as
`units.created`, `units.generated`, `units.updated` and `units.deleted`.

### Leases endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/leases` | access + `leases.view` | paginated list narrowed to the caller's scope; filters `q` (lease number, unit, property, tenant name/email), `status`, `property_id`, `unit_id`, `tenant_id`; `sort` accepts number, start_date, end_date, rent, deposit, status, unit, property, created_at, updated_at |
| `GET /api/v1/leases/{id}` | access + `leases.view` | lease with its tenant roster and deposit history |
| `GET /api/v1/leases/{id}/tenants` | access + `leases.view` | roster only, ordered primary, co-tenant, guarantor, occupant |
| `PUT /api/v1/leases/{id}/tenants` | access + `leases.update` | replaces the roster; body `{"tenants":[{"tenant_id":1,"role":"primary"}]}` |
| `POST /api/v1/leases` | access + `leases.create` | creates a **draft**; `unit_id`, `start_date`, `rent_amount` required |
| `PATCH /api/v1/leases/{id}` | access + `leases.update` | partial update, only while the lease is a draft or pending signature |
| `DELETE /api/v1/leases/{id}` | access + `leases.delete` | hard delete, draft leases only (mirrors the seeded "delete draft leases" permission) |
| `POST /api/v1/leases/{id}/activate` | access + `leases.activate` | body optional; `signed_at` overrides the default of now, and a positive deposit opens its pending record |
| `POST /api/v1/leases/{id}/terminate` | access + `leases.terminate` | `reason` required; `terminated_at` optional |
| `POST /api/v1/leases/{id}/renew` | access + `leases.renew` | body optional; returns the new lease with 201 |

A lease is created as `draft` and `status` is never a writable field — the strict
JSON decoder rejects it. Movement between the six statuses in
`chk_leases_status` happens only through activate, terminate and renew, and each
endpoint refuses rows in the wrong state with 409. Listing and detail reads obey
both halves of a caller's scope: a lease is visible if its property is assigned
to them **or** their tenant record appears on its roster, so a tenant sees their
own agreements and a caretaker who also rents sees both. An id outside that set
returns 404 rather than 403.

`unit_id` must be a live unit inside the caller's scope, and neither the unit nor
its property may be archived. `uq_leases_active_unit` (the generated
`active_unit_id` column) allows one active lease per door, so activating a second
one 409s with the number of the lease already holding it. Occupancy follows the
lease: activate sets `units.status = 'occupied'`, terminate frees it back to
`vacant`, and renew carries it straight across.

`lease_number` is optional. Left out, it is generated as
`{property code}-L-{year}-{NNN}` — the same scheme the demo data uses — falling
back to `L-{year}-{NNN}` when the property has no code; the counter is read under
a row lock and the unique index `uq_leases_org_number` has the final word. A
supplied number must be at most 50 characters with no control characters, and a
number already used in the organization 409s.

Money is rounded to cents: `rent_amount` must be greater than zero and at most
9999999999.99, `deposit_amount` between 0 and the same ceiling. `billing_day` and
`due_day` are limited to 1-28 because the schema rejects 29-31, month-ends
notwithstanding; `notice_period_days` is 0-3650. Dates are `YYYY-MM-DD`,
timestamps also accept RFC3339, and `end_date` cannot precede `start_date`. A
null `end_date` is an open-ended periodic tenancy; sending `null` on `PATCH`
clears it. Up to 20 roster entries are accepted per request, duplicates collapse,
roles are `primary`, `co_tenant`, `guarantor` or `occupant`, only one tenant can
be primary, and every `tenant_id` must belong to this organization with a live
user. Activation additionally requires a primary tenant and a positive rent.

Renewal starts where the old lease ended (one day after `end_date`, or
422 asking for `start_date` when the lease is open-ended) and runs `months`
(1-120, default 12) minus one day unless `end_date` is given. Billing and due
days, notice period, rent and deposit carry over unless the request overrides
them. The old row is flipped to `renewed` before the new `active` row is
inserted, in one transaction, so the active-unit key never collides; the roster
is copied and the money already collected stays recorded against the lease it was
paid on. Termination accepts an optional `terminated_at` (never before the
lease start) and stores the reason.

Activating a lease with a positive `deposit_amount` opens one `pending` deposit
record for it — the obligation, not the money; a payment later moves it to
`held`. A lease that already carries a deposit keeps it, so nothing doubles on a
re-activation. `date_paid` and the refund tables belong to the billing module, as
do invoices.

An hourly sweep in the API process (started next to the token purge in
`cmd/api/main.go`) flips `active` leases whose `end_date` has passed to
`expired`. Open-ended leases are never touched, and the unit stays `occupied`
because a holdover tenant is still in the door — terminate or renew is what frees
it. Writes are audited as `leases.created`, `leases.updated`, `leases.deleted`,
`leases.activated`, `leases.terminated`, `leases.renewed` and
`leases.tenants_updated`; the sweep is logged, not audited, because it has no
requesting user.

## Migration order
| #  | File                           | Tables |
|----|--------------------------------|--------|
| 01 | core_administration            | organizations, organization_domains, system_settings |
| 02 | identity_access                | users, roles, permissions, role_permissions, user_roles, refresh_tokens, user_tokens, owners, agents, tenants (PII ciphertext, lockout, rotation, per-org uniqueness) |
| 03 | seed_roles_and_settings        | seed: roles, permissions, default role matrix, default settings (KES, late fees...) |
| 04 | property_and_asset_management  | property_types, unit_types, amenities, properties, units, property_amenities, unit_amenities, parking_spots, property_agents, property_assignments |
| 05 | lease_and_tenant_placement     | leases, lease_tenants, deposits, deposit_refunds, documents, insurance_policies (one active lease per unit) |
| 06 | billing_ledger_and_payments    | payment_methods, invoices, invoice_items, payments, payment_allocations, late_fees, ledger_accounts, ledger_entries, credit_notes, tenant_credit_entries, payment_reversals, owner_statements, owner_statement_lines, owner_commissions, owner_disbursements + billing triggers |
| 07 | mpesa_integration              | mpesa_shortcodes, mpesa_account_refs, mpesa_transactions, mpesa_stk_requests |
| 08 | utilities_and_consumption      | utility_meters, utility_readings, utility_bills |
| 09 | maintenance_and_inspections    | contractors, maintenance_requests, maintenance_jobs, maintenance_expenses, maintenance_logs, inspections, inspection_items |
| 10 | communications_and_audit       | notifications, messages, reviews, audit_logs, pii_retention_policies, pii_access_logs |
| 11 | seed_reference_data            | seed: property/unit types, amenities, payment methods |

## Rules that keep this safe
1. **Never edit a migration that has run anywhere.** Add a new numbered pair instead (e.g. `000019_add_x.up.sql`).
2. Table `up`s use `CREATE TABLE IF NOT EXISTS`, table `down`s use `DROP TABLE IF EXISTS`, and seeds use `INSERT IGNORE`, so a half-finished run can be retried. Triggers are the exception: MySQL has no `CREATE TRIGGER IF NOT EXISTS`, so a retry after a partial trigger failure needs the leftover triggers dropped by hand.
3. MySQL DDL auto-commits (no transactional rollback). If a migration fails midway the DB is marked *dirty*: fix the schema, then `go run ./migrate force <last good version>` and re-run.
4. `down` drops tables in strict reverse-dependency order, so no `FOREIGN_KEY_CHECKS=0` is needed. Seed `down`s only delete rows nothing references.
5. `down` defaults to ONE step. `down -all` needs `-yes`; with `APP_ENV=production`, `down`, `goto` and `force` all need `-yes`.
6. Rolling back a table migration deletes that table's data. Back up first in production.
7. golang-migrate takes a MySQL advisory lock, so two app instances running `up` at once won't collide.
8. `audit_logs`, `ledger_entries`, `tenant_credit_entries` and `pii_access_logs` are append-only (triggers reject UPDATE/DELETE). Grant the app DB user only INSERT/SELECT on them, and use a separate migration user for DDL.
9. The chain targets MySQL 8.0.16+ (CHECK constraints, generated columns, `SIGNAL`). `users` is unique per organization while live and a unit can hold only one active lease, so importing legacy rows with duplicates fails at insert time — clean the data before loading it.

## Design notes vs. the files you shared
- Added `organization_id` (multi-tenancy) to top-level tables; child tables inherit it via their parent.
- `rent_payments` is replaced by `payments` + `payment_allocations` (one payment can settle several invoices); `late_fees` now hangs off `invoices`.
- `inspection_items.condition` renamed `condition_rating` (`condition` is reserved in MySQL).
- `leases.rent/deposit` became `rent_amount/deposit_amount`.
- Kenya specifics: KES default, counties/estates, KRA PIN, national ID, E.164 phones, EARB licence, M-Pesa C2B/STK tables, DSQ/bedsitter lookups.
- `invoices` is unique on `(lease_id, invoice_type, billing_period_start)`, so a monthly billing job can safely re-run.
- Status columns are VARCHAR + CHECK (no ENUM); CHECKs need MySQL 8.0.16+.

## What the schema enforces
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
