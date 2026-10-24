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

Demo login: `demo@kodipms.co.ke` / `Demo@2026!`, on the `localhost` domain
(`127.0.0.1` is a second domain for the same organization). A second, fully
separate organization is also seeded: `demo2@kodipms.co.ke`, same password, on
`demo2.localhost` -- it has its own property, lease, invoice, paybill and
M-Pesa queue, so per-organization isolation and shortcode-based callback routing
are both visible in the data.
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

### Billing endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/invoices` | access + `invoices.view` | paginated; filters `q` (invoice number, lease, unit, tenant), `status`, `invoice_type`, `lease_id`, `property_id`, `unit_id`, `tenant_id`, `overdue`, `due_from`, `due_to`; `sort` accepts number, period, issue_date, due_date, total, paid, balance, status, type, lease, unit, property, created_at, updated_at |
| `GET /api/v1/invoices/{id}` | access + `invoices.view` | invoice with lines, allocations and late fees |
| `GET /api/v1/invoices/{id}/ledger` | access + `invoices.view` | the journal that backs this invoice |
| `POST /api/v1/invoices` | access + `invoices.create` | `lease_id` + at least one line; `issue: true` posts it straight out of draft |
| `PATCH /api/v1/invoices/{id}` | access + `invoices.update` | draft only; `items` replaces the whole line set and re-prices the invoice |
| `DELETE /api/v1/invoices/{id}` | access + `invoices.delete` | deletes an unpaid draft; anything with money against it 409s |
| `POST /api/v1/invoices/{id}/issue` | access + `invoices.update` | body optional; `issue_date` / `due_date` overrides; writes the issue journal |
| `POST /api/v1/invoices/{id}/void` | access + `invoices.update` | body optional `reason`; mirrors the issue journal and waives open late fees |
| `POST /api/v1/invoices/{id}/late-fee` | access + `invoices.update` | body optional; `amount` overrides the settings formula, else `fixed` or `percent` of rent |
| `POST /api/v1/invoices/{id}/apply-credit` | access + `payments.reconcile` | body optional `amount`; spends the lease's unallocated payments on this invoice |
| `POST /api/v1/late-fees/{id}/waive` | access + `invoices.update` | `reason` required; takes the fee back off the invoice and the ledger |
| `GET /api/v1/payments` | access + `payments.view` | filters `q`, `status`, `lease_id`, `property_id`, `method_id`, `from`, `to`; `sort` accepts paid_at, amount, reference, status, method, lease, created_at |
| `GET /api/v1/payments/{id}` | access + `payments.view` | payment with its allocations and reversal |
| `POST /api/v1/payments` | access + `payments.create` | `lease_id`, `payment_method_id`, `amount`; `allocations` optional, else oldest-first; `paid_at` optional and never in the future |
| `POST /api/v1/payments/{id}/reverse` | access + `payments.reverse` | `reason` required; a reversal is always the whole payment |
| `GET /api/v1/leases/{id}/credit` | access + `payments.view` | the credit entries a lease has, newest first |
| `GET /api/v1/credit-notes` | access + `invoices.view` | filters `q`, `status`, `lease_id`, `invoice_id`, `property_id` |
| `GET /api/v1/credit-notes/{id}` | access + `invoices.view` | note with the journal it posted |
| `POST /api/v1/credit-notes` | access + `invoices.create` | creates a **draft**; `lease_id` and `amount` required |
| `PATCH /api/v1/credit-notes/{id}` | access + `invoices.update` | draft only; `invoice_id` stops being writable once the note is issued |
| `DELETE /api/v1/credit-notes/{id}` | access + `invoices.delete` | draft notes only |
| `POST /api/v1/credit-notes/{id}/issue` | access + `invoices.update` | body optional `invoice_id`; freezes lease, invoice, amount and date |
| `POST /api/v1/credit-notes/{id}/apply` | access + `invoices.update` | reduces the invoice and posts the note |
| `POST /api/v1/credit-notes/{id}/void` | access + `invoices.update` | body optional `reason`; only an issued note can be voided, a draft is deleted |
| `GET /api/v1/payment-methods` | access + `payments.view` | reference list, `is_active` included |
| `GET /api/v1/ledger/entries` | access + `invoices.view` | filters `account`, `source_type`, `source_id`, `lease_id`, `property_id`, `from`, `to` |
| `GET /api/v1/ledger/accounts` | access + `invoices.view` | the chart of accounts |
| `GET /api/v1/billing/summary` | access + `invoices.view` | billed / collected / outstanding / overdue / credit plus invoice counts, in the organization's own date |

**Money is never a writable field.** `invoices.amount_paid` is a trigger-derived
column and `balance` is generated from it, so no request can invent a payment;
`status` moves only through issue, void, apply and the sweep. A client sends a
line's `quantity`, `unit_price` and `tax_rate` and the server computes the amount,
the subtotal, the tax and the total — an `amount` in the body is rejected by the
strict decoder. Lines are capped at 60 per invoice, `tax_rate` at 100, and every
figure at 9999999999.99.

**A utility charge enters through a line, not through the meter.** A line may carry
`source_type: "utility_bill"` and the `source_id` of a `utility_bills` row; the bill
then leaves utilities `pending` and becomes `billed`, pointing back at the invoice that
holds it. Billing checks the claim, not the dial: the bill must exist in the same
organization, sit on the invoice's own unit, and be neither paid, void, nor already
carried by another invoice, or the line 422s. Replacing the line set re-syncs the link,
and voiding or deleting an invoice hands its `billed` bills back to utilities to be
raised again — a `paid` one keeps its link, because the money it collected has not gone
anywhere.

**Every movement of money is a balanced journal.** One transaction writes the
rows and the entries: issue (AR debit against rent/utility/service income,
deposit lines to the deposit liability), payment (cash debit against AR, with any
unallocated remainder to the unallocated-receipts account), reversal (the mirror),
late fee, credit note and credit application. `PostJournal` refuses an unbalanced
set before writing anything, and `ledger_entries` is append-only in SQL, so a
correction is always another entry, never an edit.

**Payments and allocations follow the triggers.** `trg_allocations_validate`
rejects an allocation whose payment and invoice sit on different leases, points at
a draft or void invoice, or exceeds the invoice total; `trg_allocations_sync_invoice`
then recomputes `amount_paid`. So the module never writes `amount_paid` and lets
the allocation insert fail loudly (409) instead of pre-computing it. `payments`
reference is unique per method, and a duplicate receipt 409s rather than
double-counting. `paid_at` is bounded by the organization's own calendar: a
receipt dated after its today, or before 2000, 422s, because an unbounded
`paid_at` moves the journal period, the aging report and the oldest-first credit
order along with it. The `status` filter accepts the whole column vocabulary
(`pending`, `completed`, `failed`, `reversed`) even though the module only ever
books a receipt as `completed` — the gateway statuses are listed so a caller can
filter for them before anything writes one. A reversal must be the full payment (`trg_payment_reversals_validate`),
which is why `ReversePayment` takes no amount — a partial refund is a credit note.
The unspent half of a reversed payment is clawed back with a negative
`tenant_credit_entries` row of type `reversal`; tenant credit is always *derived*
from payments minus live allocations, so it cannot be padded.

**Credit notes stay out of the money the tenant holds.** They reduce the invoice
they are applied to and post income/AR lines; they never add `invoice_items` and
never write a credit entry, because credit comes from overpaid money.
`trg_credit_notes_issued_immutable` freezes `lease_id`, `invoice_id`, `amount` and
`issue_date` the moment a note leaves draft, and `applied`/`void` are final — so
the API only accepts `invoice_id` while drafting, or in the same call that issues
the note.

**Automation, in the organization's own day.** An hourly sweep (started next to
the lease purge in `cmd/api/main.go`) does three things per organization, using
`organizations.timezone` for "today": it raises missing rent periods, marks unpaid
invoices `overdue`, and assesses the late fee once the grace period has passed.
A period is raised as a **draft** five days before it starts — so a meter reading
or a service charge can still be attached — and issued on the day it begins, which
is when the journal is written. Rent is billed gross (residential rent is not a
VAT supply); the security deposit rides on the first rent invoice while the
`pending` deposit record still is pending, and a fully-paid invoice holding a
deposit line flips that deposit to `held` exactly once.

The sweep is safe to run everywhere and to re-run: `uq_invoices_lease_period`
(lease, type, period start) and `uq_late_fees_invoice_date` make a duplicate a
no-op, the issuing statement is guarded on `status = 'draft' AND amount_paid = 0`
so a second instance posts no second journal, each lease is its own transaction,
and a lease with nothing to bill returns `errNothingToBill` and is logged at debug.
Backlog is capped at three periods per run so a month of downtime cannot flood the
ledger, and rent periods clamp to day 28 to match `chk_leases_billing_day` and
`chk_leases_due_day`. Sweep writes are logged, not audited — there is no
requesting user.

**Scope is enforced in SQL, not in the response.** Every query carries
`organization_id`, and `scopeFilter` narrows by the caller's assigned properties —
or, for a tenant, to leases their own tenant record sits on, with drafts hidden
entirely (`hideDrafts`), `status=draft` refused as a filter and `PATCH` on a
foreign invoice returning 404 rather than 403. Ledger, credit-note and payment
reads all take the same scope. Reads need `invoices.view`/`payments.view`; a
tenant role has both and therefore sees only its own money.

Audited as `invoices.created`, `invoices.updated`, `invoices.deleted`,
`invoices.issued`, `invoices.voided`, `invoices.late_fee_assessed`,
`late_fees.waived`, `payments.credit_applied`, `payments.recorded`,
`payments.reversed`, `credit_notes.created`, `credit_notes.updated`,
`credit_notes.issued`, `credit_notes.applied`, `credit_notes.voided` and
`credit_notes.deleted`, each with the before/after values the request changed.

### Utilities endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/utility/meters` | access + `utilities.view` | paginated; filters `q` (meter number, property, unit), `utility_type`, `status`, `property_id`, `unit_id`, `open_only`; `sort` accepts number, type, unit, property, rate, status, last_reading, created_at, updated_at |
| `GET /api/v1/utility/meters/lookups` | access + `utilities.view` | the vocabularies plus the caller's units and meters, one request for every picker |
| `GET /api/v1/utility/meters/{id}` | access + `utilities.view` | meter with its last 50 readings and the last 50 bills raised from it |
| `POST /api/v1/utility/meters` | access + `utilities.create` | `property_id`, `utility_type`, `meter_number`; no `unit_id` means a shared property-level meter, `unit_of_measure` defaults per type (`m3` / `kWh` / `unit`), `rate_per_unit` is the tariff |
| `PATCH /api/v1/utility/meters/{id}` | access + `utilities.update` | `unit_id: 0` detaches the meter to the property; changing `utility_type` 409s once the meter has been read |
| `DELETE /api/v1/utility/meters/{id}` | access + `utilities.update` | only a meter that has never been read or billed; a meter with history 409s — set it `faulty` instead |
| `GET /api/v1/utility/meters/{id}/readings` | access + `utilities.view` | filters `from`, `to`; `sort` accepts date, consumption, current, created_at |
| `POST /api/v1/utility/meters/{id}/readings` | access + `utilities.record_reading` | `reading_date` + `current_reading`; `previous_reading` defaults to the last reading on the meter, a future date 422s and a second reading on the same day 409s |
| `GET /api/v1/utility/readings/{id}` | access + `utilities.view` | reading with its meter, rate and the bill it produced; `adjustable` says whether it can still be corrected |
| `PATCH /api/v1/utility/readings/{id}` | access + `utilities.record_reading` | `current_reading` / `notes` only — the reading date never moves; the next reading's `previous_reading` follows the correction |
| `DELETE /api/v1/utility/readings/{id}` | access + `utilities.record_reading` | unbilled readings only; the reading after it inherits the deleted row's starting figure so the chain stays continuous |
| `GET /api/v1/utility/bills` | access + `utilities.view` | filters `q`, `status`, `type`, `property_id`, `unit_id`, `meter_id`, `overdue`, `due_from`, `due_to`; `sort` accepts due, amount, type, status, period, unit, property, created_at, updated_at |
| `GET /api/v1/utility/bills/{id}` | access + `utilities.view` | bill with the active lease and tenant on the unit, the invoice link and the overdue flag |
| `POST /api/v1/utility/bills` | access + `utilities.create` | either `unit_id` + `type` + `amount` + dates, or `unit_id` + `reading_id` and the type, amount and period are taken from the dial |
| `POST /api/v1/utility/bills/generate` | access + `utilities.create` | one bill per unbilled reading on an active, unit-attached meter; `meter_id`, `property_id`, `unit_id`, `from`, `to` narrow it; returns `{created, skipped}` |
| `PATCH /api/v1/utility/bills/{id}` | access + `utilities.update` | pending only; anything billing has taken 409s |
| `POST /api/v1/utility/bills/{id}/void` | access + `utilities.update` | body optional `reason`; refuses a bill already carried onto an invoice |
| `GET /api/v1/utility/summary` | access + `utilities.view` | meters, open readings, pending bill count/total and billed total; `property_id` optional |

**A meter is only ever read.** Nothing in this module creates money: `utility_meters`
carries the tariff, `utility_readings` the quantity and `utility_bills` the charge
that quantity implies. A bill becomes receivable only when billing attaches it to an
invoice line, which is why `status` is not a client field — `pending` becomes `billed`
in that hand-over and `void` only through the void call. `invoice_id` is written by
billing, never here, and a bill that already carries one cannot be edited or voided
from this API.

**The quantity is the schema's, not the caller's.** `utility_readings.consumption` is a
generated column (`current - previous`) and `chk_readings_order` refuses a reading that
goes backwards, so the module defaults `previous_reading` from the meter's own history
and caps a correction at the next reading's value instead of trusting the figure sent.
A correction moves the reading after it as well, and is refused as soon as either side
of it carries a bill — the quantity a charge was priced from stays fixed.

**Bills follow the readings they came from.** `uq_ubills_reading` allows one bill per
reading, so re-running `generate` is a no-op rather than a double charge. A generated
bill is dated on the tenant's rent `due_day` in the month after the reading, priced at
the meter's rate, and skipped (counted, not dropped silently) when the unit has no
active lease, nothing was consumed, or no rate is set. Hand-raised bills take the same
defaults from a `reading_id` and may still override the amount.

**Deletion is where the schema is least forgiving, so it is the least allowed.** A
meter delete cascades to its readings and orphans the bills raised from them, so a
meter with history 409s; the seed has no `utilities.delete` permission at all, which
is why meter deletes ride on `utilities.update` and reading deletes on
`utilities.record_reading`. A reading delete is only accepted while neither it nor the
reading after it has been billed.

**Scope is enforced in SQL.** `utility_*` tables carry no `organization_id`: meters and
readings reach an organization through `properties`, bills through `units` →
`properties`, and every query also filters `deleted_at IS NULL` on those parents. A
tenant's scope is the union of their assigned properties and the units their own tenant
record is leased on, which leaves a shared property-level meter invisible to them —
they see the charges on their door, not the building's dial. `404`, not `403`, for a
row outside the scope.

Audited as `utility_meters.created`, `utility_meters.updated`,
`utility_meters.deleted`, `utility_readings.created`, `utility_readings.updated`,
`utility_readings.deleted`, `utility_bills.created`, `utility_bills.updated`,
`utility_bills.voided` and `utility_bills.generated`, each with the before/after values
the request changed.

### M-Pesa endpoints
| Method & path | Auth | Notes |
|---|---|---|
| `GET /api/v1/mpesa/shortcodes` | access + `mpesa.view` | paginated; filters `q`, `environment`, `type`, `status` (`active`/`inactive`); `sort` accepts shortcode, environment, type, status, created_at, updated_at. Credentials are never returned, not even masked — the row answers with `has_consumer_key`, `has_passkey` and friends |
| `GET /api/v1/mpesa/shortcodes/lookups` | access + `mpesa.view` | the vocabularies, the three callback paths with a `{TOKEN}` placeholder, and `gateway_configured` / `billing_connected`, which say whether this installation can prompt a phone and can book a receipt |
| `GET /api/v1/mpesa/shortcodes/{id}` | access + `mpesa.view` | |
| `POST /api/v1/mpesa/shortcodes` | access + `mpesa.manage` | `shortcode` (4–20 digits), `account_type`, `environment`, optional `initiator_name` and the three Daraja secrets. **The response contains the callback token once** and the three URLs to paste into the Daraja portal; nothing can read it back afterwards |
| `PATCH /api/v1/mpesa/shortcodes/{id}` | access + `mpesa.manage` | secrets are replace-or-keep: sending any one of `consumer_key`, `consumer_secret`, `passkey` requires all three (they belong to one Daraja app), and omitting them leaves them alone. Changing `shortcode`, `environment` or `account_type` 409s once the paybill has refs, receipts or prompts attached |
| `POST /api/v1/mpesa/shortcodes/{id}/rotate-token` | access + `mpesa.manage` | mints a new token and returns it; every URL already handed to Safaricom stops working the moment this answers, so rotate last |
| `DELETE /api/v1/mpesa/shortcodes/{id}` | access + `mpesa.manage` | only a shortcode nothing references; anything else 409s with the counts and says to deactivate it instead |
| `GET /api/v1/mpesa/account-refs` | access + `mpesa.view` | filters `q`, `shortcode_id`; `sort` accepts reference, shortcode, lease, created_at, updated_at |
| `POST /api/v1/mpesa/account-refs` | access + `mpesa.manage` | `shortcode_id`, `lease_id` (in the caller's scope), `account_reference`; one reference routes one lease per paybill, so a number already in use on that shortcode 409s |
| `DELETE /api/v1/mpesa/account-refs/{id}` | access + `mpesa.manage` | removes the routing row; receipts already recorded against it are untouched |
| `GET /api/v1/mpesa/receipts` | access + `mpesa.view` | the C2B ledger and the reconcile queue. Filters `q` (receipt, account ref, phone, name), `status`, `shortcode_id`, `lease_id`, `from`, `to`; `sort` accepts time, amount, status, ref, msisdn, created, updated. A receipt with no lease is visible only to a caller with all-property scope |
| `GET /api/v1/mpesa/receipts/{id}` | access + `mpesa.view` | includes the raw callback payload exactly as Safaricom sent it |
| `POST /api/v1/mpesa/receipts/{id}/match` | access + `mpesa.reconcile` | `{lease_id}`: books the receipt through billing and links it. Refuses a receipt already matched or ignored, and refuses a lease the caller cannot see |
| `POST /api/v1/mpesa/receipts/{id}/ignore` | access + `mpesa.reconcile` | `{reason}` required. The row stays; money that belongs to someone else is returned by whoever received it, not by this API |
| `GET /api/v1/mpesa/stk` | access + `mpesa.view` | prompts and their outcomes; filters `q`, `status`, `lease_id`, `from`, `to`; `sort` accepts created, updated, amount, status, phone, checkout |
| `POST /api/v1/mpesa/stk/push` | access + `mpesa.push` | `shortcode_id`, `lease_id`, optional `invoice_id`, `amount`, `phone`, `description`. No amount means the invoice's own balance; no phone means the primary tenant's. Writes nothing until Daraja accepts the request |
| `GET /api/v1/mpesa/stk/{id}` | access + `mpesa.view` | |

**Webhooks are not API endpoints.** `POST /integrations/mpesa/c2b/{token}/validate`,
`POST /integrations/mpesa/c2b/{token}/confirm` and `POST
/integrations/mpesa/stk/{token}/callback` are mounted on the root router, outside
`/api/v1`, because a callback arrives with no host that resolves an organization and no
bearer token. The token in the path is the only credential, is looked up by its keyed
HMAC (`callback_token_hash`, indexed), and must match the shortcode named in the body —
a valid token for paybill A cannot book money against paybill B. An unknown token and a
deactivated shortcode get the same answer. The group takes the same per-IP rate limit as
the API, and nothing else.

Replies are always HTTP 200 with `{"ResultCode":0|1,"ResultDesc":"..."}`. A non-2xx
reads as a delivery failure and Safaricom retries the same receipt for hours; the
verdict belongs in `ResultCode`.

**Receipts are recorded before they are spent.** `mpesa_transactions` is unique on
`trans_id` across the whole installation, so the insert is what a retried callback bumps
into, and it happens before any money moves. Only then does billing book the payment,
and only then is the receipt linked to it. If the process dies in between, a payment
exists and the receipt is still unmatched, which a person can fix; in the other order a
retry could pay the same rent twice. A duplicate `trans_id` is re-read, and a receipt
already owned by another organization is refused outright — that is what a shared
sandbox paybill looks like from the inside, and it is also what a forged callback looks
like.

**A prompt and a confirmation can describe the same shilling.** A paybill with C2B
confirmation switched on receives one for a payment that started as an STK push. The
rule above is what makes that safe: whichever callback arrives first inserts the receipt
and books the money, the second finds `trans_id` taken with a payment attached, and both
point at the one payment rather than creating two. `uq_payments_org_method_reference` is
the backstop if the process dies between booking and linking — the payment is then found
by its reference and linked, not created again.

**Money only enters through billing.** A receipt is booked over the
`mpesa.PaymentBooker` seam, wired in `internal/routes/mpesa_gateway.go`, the only file
that knows both modules — domain packages still do not import each other. Allocation,
tenant credit, journal entries and the billing triggers are the same ones a cashier goes
through, and an installation with no seam refuses to book rather than quietly dropping
the money. Refunds are not this module's job: a reversal belongs in billing, where the
allocation it undoes lives.

**Validation stores nothing.** It answers whether the account number is one this
organization routes to a live lease, and stops there. A tenant can still walk away from
a validation, so a row for each one would fill the queue with payments that never
happened. A till has no account number to check, so an empty one is accepted.

**The unmatched queue is the design, not an error.** A confirmation whose account number
is unknown, whose reference is retired, or whose lease cannot be resolved is recorded and
left `unmatched`, and the callback is still answered as accepted — the money is real, and
refusing it would tell the tenant their rent failed. Whoever holds `mpesa.reconcile`
decides what it was for.

**Pushing needs three things this repository cannot give you.** Consumer
key/secret/passkey from a Daraja app, an organization that has registered a sandbox
paybill, and `PUBLIC_BASE_URL` set to a URL Daraja can reach — a tunnel URL works in
development, and it is baked into the outbound `CallBackURL` when the prompt is sent, so
a shortcode created before the base URL changed needs its token rotated to get a
callback URL that resolves. `TRUST_PROXY` has to be right too: callbacks come from
Safaricom's addresses, not from your reverse proxy, or the IP rate limiter bans the
network's own retries. OAuth tokens for a Daraja app are cached for 25 minutes out of
the hour Daraja grants. Sandbox callbacks are not reliably delivered, and the sandbox is
shared, so a number there proves nothing about an organization.

**Seeded shortcodes have no credentials and no token.** `demo_seed.sql` inserts paybill
`600000` (sandbox, org 1) and `400200` (production, org 9002) with references, receipts
and a prompt, so the lists and the queue have data. `POST
/api/v1/mpesa/shortcodes/{id}/rotate-token` is what gives a demo shortcode a token, and
pushing still needs real Daraja credentials on the row.

Audited as `mpesa_shortcodes.created`, `mpesa_shortcodes.updated`,
`mpesa_shortcodes.token_rotated`, `mpesa_shortcodes.deleted`,
`mpesa_account_refs.created`, `mpesa_account_refs.deleted`,
`mpesa_transactions.matched`, `mpesa_transactions.unmatched`,
`mpesa_transactions.ignored`, `mpesa_stk_requests.pushed` and
`mpesa_stk_requests.<status>`, each with the values the request changed. Callback rows
record the organization and no user — there is none.

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
1. **The chain is consolidated into `000001`–`000011` and nothing is deployed yet.** Put a schema
   fix in the base pair that owns the table (keep `CREATE TABLE IF NOT EXISTS` / `DROP TABLE IF
   EXISTS` and each trigger create/drop symmetric) instead of appending `000012+`. Once the schema
   has reached a real environment, switch to additive numbered pairs and never edit a pair that ran.
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
- **Utility charges stay traceable to a dial.** A meter number is unique per property
  (`uq_meters_property_number`), a reading unique per meter per day
  (`uq_readings_meter_date`), and `uq_ubills_reading` allows exactly one bill per
  reading — which is what makes the generate job re-runnable. `consumption` is a
  generated column and `chk_readings_order` refuses a reading that goes backwards.
  `utility_*` rows inherit their organization through `properties` and `units`, and the
  meter/reading/invoice links on a bill are `SET NULL` so a charge outlives the dial
  that produced it; only the meter→readings link cascades, which is why deleting a
  meter with history is refused in code.
- **Owner money is explicit.** `owner_statements` + lines, `owner_commissions` and
  `owner_disbursements` record what each owner is owed and paid; `owners.payout_*`
  columns are encrypted.
- **M-Pesa is routed, not guessed.** `mpesa_shortcodes` holds per-org, per-environment
  credentials (encrypted) and the callback token hash; `mpesa_account_refs` maps
  `BillRefNumber` to a lease. Transactions and STK requests carry both links, and
  `UNIQUE (organization_id, payment_id)` on each of them means one payment can be backed
  by only one confirmation of that channel — a second receipt cannot reuse it.
  **No Go code addresses these tables yet.** Three things are already settled by the
  schema and the router: a Safaricom callback arrives at one fixed URL, so it must be
  mounted outside the host-based tenancy resolver (`/api/v1` refuses any `Host` that is
  not a registered organization domain) and take its organization from the shortcode or
  the `CheckoutRequestID`, never from the request; `mpesa_transactions.organization_id`
  is `NOT NULL`, so a callback whose shortcode and `BillRefNumber` both fail to resolve
  has nowhere to be stored and must be refused with an alert rather than queued; and
  `callback_token_hash` is the lowercase hex of `internal/platform/pii`'s keyed
  HMAC-SHA256, looked up through `idx_mpesa_shortcodes_token`. Shortcodes stay unique
  across organizations because a callback carries a shortcode and nothing else, which
  also means only one organization can hold a sandbox shortcode at a time — Daraja hands
  every test account the same paybill.
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
