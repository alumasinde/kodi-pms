# Property Management System - Code Guide (Kenya)

Backend implementation guide for the rental / property management platform: Go + MySQL 8 + Redis, built on the schema in `db/migrations` (migrations 000001-000014).
Each module section gives: **purpose, endpoints, who can do what, what it queries, what it returns, Redis usage, code skeleton, and automations.**

> The code is a pattern to copy, not a framework lock-in. Examples use `net/http` + `chi`, `sqlx`, `go-redis/v9`, `shopspring/decimal`. Swap freely, but keep the layering and the rules in section 1.

## Contents
1. [Foundations: layout, layers, conventions](#1-foundations)
2. [Request pipeline and Redis map](#2-request-pipeline-and-redis-map)
3. [Auth module](#3-auth-module)
4. [RBAC: roles, permissions, data scope, what each role sees](#4-rbac)
5. [Settings module](#5-settings-module-org-property-roles-permissions)
6. [Users module](#6-users-module)
7. [Properties module](#7-properties-module)
8. [Units module (autofill)](#8-units-module)
9. [Tenants module](#9-tenants-module)
10. [Leases module (the core workflow)](#10-leases-module)
11. [Billing and Payments (invoices, M-Pesa)](#11-billing-and-payments)
12. [Utilities and Maintenance](#12-utilities-and-maintenance)
13. [Notifications module](#13-notifications-module)
14. [Dashboard module](#14-dashboard-module)
15. [Automation catalogue (scheduled jobs)](#15-automation-catalogue)
16. [Testing and release checklist](#16-testing-and-release-checklist)

---

## 1. Foundations

### 1.1 Repo layout
```
cmd/api/main.go            # wiring: config, db, redis, router, jobs
migrate/main.go            # migration runner
db/migrations/             # .up.sql / .down.sql
internal/
  platform/                # config, db, redis, httpx (responses/errors), logger, clock
  tenancy/                 # org resolution from Host header
  auth/  rbac/             # login, tokens, principal, permission + scope
  settings/ users/ properties/ units/ tenants/ leases/
  billing/ payments/ utilities/ maintenance/
  notifications/ dashboard/ audit/
  jobs/                    # scheduled automations
```
Every module has the same four files:

| File | Responsibility | May NOT |
|---|---|---|
| `handler.go` | decode request, validate, call service, encode response | contain SQL or business rules |
| `service.go` | business rules, transactions, defaults/autofill, events | touch `http.Request` |
| `repo.go` | SQL only; every method takes a `Scope` | decide permissions |
| `dto.go` | request/response structs, role-aware serializers | |

### 1.2 Golden rules
1. **Tenant isolation is never optional.** Every query on org-owned data filters `organization_id` (directly or through `properties`). A repo method without a `Scope` argument is a bug.
2. **Permission says *what you may do*, scope says *which rows*.** `leases.view` + scope "properties 3,7" = leases of those two properties only.
3. **Never trust the client for money, status, or org.** Recompute rent, deposits, totals, numbers server-side; ignore `organization_id` in request bodies.
4. **Anything that touches several tables is one DB transaction.** Side effects (SMS, cache bust, audit) happen **after commit**.
5. **Idempotent by construction.** Jobs and webhooks can fire twice; unique keys in the schema (`invoices (lease_id, invoice_type, billing_period_start)`, `mpesa_transactions.trans_id`, `payments (payment_method_id, reference)`) make the second run a no-op.
6. **Money = `decimal.Decimal`** (JSON as string `"12500.00"`). Never `float64`.
7. **Time**: DB timestamps are UTC; business dates (`due_date`, `start_date`) are `DATE` in the org timezone (`Africa/Nairobi`). Jobs compute "today" using the org timezone.
8. **Soft delete** (`deleted_at`) on users, properties, units, organizations: every read adds `deleted_at IS NULL`.

### 1.3 Response envelope
```json
{ "data": { }, "meta": { "page": 1, "per_page": 25, "total": 312 } }
{ "error": { "code": "validation_failed", "message": "Invalid input", "fields": { "start_date": "required" } } }
```
Pagination: `?page=&per_page=` (max 100), `?sort=-created_at` validated against a per-module whitelist, never interpolated raw into SQL.

```go
// internal/platform/httpx/httpx.go
type Err struct{ Status int; Code, Message string; Fields map[string]string }
func (e *Err) Error() string { return e.Message }

var (
	ErrUnauthorized = &Err{401, "unauthorized", "Authentication required", nil}
	ErrForbidden    = &Err{403, "forbidden", "You do not have permission", nil}
	ErrNotFound     = &Err{404, "not_found", "Resource not found", nil}
	ErrConflict     = func(m string) *Err { return &Err{409, "conflict", m, nil} }
)

func OK(w http.ResponseWriter, data any, meta any) { write(w, 200, map[string]any{"data": data, "meta": meta}) }
func Fail(w http.ResponseWriter, err error) {
	var e *Err
	if !errors.As(err, &e) { log.Error().Err(err).Msg("internal"); e = &Err{500, "internal", "Something went wrong", nil} }
	write(w, e.Status, map[string]any{"error": e})
}
```
**Use 404 (not 403) when a row exists but is outside the caller's scope**, so ids can't be probed.

### 1.4 Core types shared by every module
```go
// internal/rbac/principal.go
type Scope struct {
	OrgID         uint64
	AllProperties bool     // org_admin, accountant, system_admin
	PropertyIDs   []uint64 // landlord (owned), manager/caretaker (assigned), agent (assigned)
	TenantID      *uint64  // tenant: only their own leases/invoices/requests
	ContractorID  *uint64  // contractor: only jobs assigned to them
}

type Principal struct {
	UserID uint64
	OrgID  uint64
	Roles  []string
	Perms  map[string]struct{}
	Scope  Scope
}
func (p *Principal) Can(perm string) bool { _, ok := p.Perms[perm]; return ok }

// Fragment helper: use in every repo that joins properties.
func (s Scope) PropertyFilter(col string) (string, []any) {
	if s.AllProperties { return "1=1", nil }
	if len(s.PropertyIDs) == 0 { return "1=0", nil }
	ph := strings.TrimSuffix(strings.Repeat("?,", len(s.PropertyIDs)), ",")
	args := make([]any, len(s.PropertyIDs))
	for i, id := range s.PropertyIDs { args[i] = id }
	return col + " IN (" + ph + ")", args
}
```

### 1.5 Standard repo pattern
```go
func (r *Repo) List(ctx context.Context, sc rbac.Scope, f UnitFilter) ([]Unit, int, error) {
	frag, args := sc.PropertyFilter("p.id")
	q := `SELECT u.id, u.number, u.status, u.rent, p.name AS property_name
	      FROM units u JOIN properties p ON p.id = u.property_id
	      WHERE p.organization_id = ? AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND ` + frag
	args = append([]any{sc.OrgID}, args...)
	if f.Status != "" { q += " AND u.status = ?"; args = append(args, f.Status) }
	q += " ORDER BY " + f.SafeSort(unitSortCols) + " LIMIT ? OFFSET ?"
	args = append(args, f.Limit(), f.Offset())
	var out []Unit
	return out, 0, r.db.SelectContext(ctx, &out, q, args...)
}
```

---

## 2. Request pipeline and Redis map

### 2.1 Middleware order (outermost first)
```
RequestID -> Recover -> Logger -> CORS -> RateLimit(IP) -> ResolveOrg(Host)
 -> Authenticate (JWT -> Principal) -> RateLimit(user) -> Require(perm) -> Idempotency (POST) -> handler
```
```go
r := chi.NewRouter()
r.Use(mw.RequestID, mw.Recover, mw.Logger, mw.CORS, mw.RateLimitIP(rdb, 300, time.Minute), tenancy.Resolve(rdb, db))
r.Route("/api/v1", func(r chi.Router) {
	auth.Mount(r)                                   // public: login, refresh, forgot, accept-invite
	r.Group(func(r chi.Router) {
		r.Use(authn.Authenticate, mw.RateLimitUser(rdb, 600, time.Minute))
		r.With(rbac.Require("units.view")).Get("/units", units.H.List)
		r.With(rbac.Require("units.create"), mw.Idempotent(rdb)).Post("/units", units.H.Create)
		// ...
	})
})
// Public webhooks (Safaricom) live OUTSIDE this group, protected by secret path + IP allow-list.
```

### 2.2 Redis key map
| Key | Value | TTL | Written / invalidated |
|---|---|---|---|
| `domain:{host}` | `{org_id, name, logo, currency}` | 1h | resolve miss; DEL on `organization_domains` change |
| `authzver:{orgID}` | int version | none | `INCR` whenever roles/permissions/assignments change |
| `authz:{userID}:{ver}` | roles, perms, scope (JSON) | 10m | built on first request after login/change; the version in the key makes stale entries unreachable |
| `jwt:deny:{jti}` | 1 | until token expiry | logout / password change |
| `rl:login:{ip}:{email}` | counter | 15m | login failures; lock at 5 |
| `rl:api:{userID}` / `rl:ip:{ip}` | counter | 1m | rate limiter |
| `otp:{phone}` | hashed code + attempts | 5m | optional SMS OTP |
| `settings:org:{orgID}` | hash of org-level settings | 10m | DEL on settings write |
| `settings:prop:{propID}` | hash of property overrides | 10m | DEL on property settings write |
| `lookup:unit_types`, `lookup:amenities`, `lookup:property_types`, `lookup:payment_methods` | JSON | 1h | DEL when edited |
| `dash:{orgID}:{scopeHash}:{metric}` | JSON | 60-300s | TTL + targeted DEL on payment/lease events |
| `idem:{userID}:{key}` | stored response | 24h | `Idempotency-Key` header on POSTs |
| `lock:job:{name}:{date}` | instance id | job length | `SET NX EX`; stops two replicas running the same cron |
| `notif:stream` | Redis Stream | - | notification queue (consumer group `workers`) |
| `notif:dead` | Redis Stream | - | dead letters after max retries |

```go {4-6}
// Always the same cache-aside helper; fall back to DB if Redis is down (degrade, don't fail).
func Cached[T any](ctx context.Context, rdb *redis.Client, key string, ttl time.Duration, load func() (T, error)) (T, error) {
	var v T
	if s, err := rdb.Get(ctx, key).Result(); err == nil && json.Unmarshal([]byte(s), &v) == nil { return v, nil }
	v, err := load()
	if err != nil { return v, err }
	if b, e := json.Marshal(v); e == nil { rdb.Set(ctx, key, b, ttl) }
	return v, nil
}


## 3. Auth module

**Purpose:** log users in, issue short-lived access tokens, rotate refresh tokens, reset passwords, onboard invited users, and tell the frontend what the user is allowed to see.

### 3.1 Endpoints
| Method & path | Auth | Purpose |
|---|---|---|
| `POST /auth/login` | public | email or phone + password -> tokens |
| `POST /auth/refresh` | refresh token | rotate refresh token, new access token |
| `POST /auth/logout` | access | revoke refresh family, deny-list access jti |
| `POST /auth/forgot-password` | public | always returns 200 (no account enumeration); sends SMS/email link |
| `POST /auth/reset-password` | token | set new password, revoke all sessions |
| `POST /auth/accept-invite` | token | invited user sets password |
| `POST /auth/change-password` | access | also revokes other sessions |
| `GET /auth/me` | access | profile + roles + permissions + scope summary + org branding |

### 3.2 Token design
- **Access token**: JWT, 15 min, claims `sub` (user id), `org`, `jti`. **No permissions inside**, so permission changes take effect immediately.
- **Refresh token**: 32 random bytes, base64url, returned once. Only `sha256(token)` is stored in `refresh_tokens`. 30 days. Web: `HttpOnly; Secure; SameSite=Lax` cookie. Mobile: secure storage.
- **Rotation + reuse detection**: each refresh revokes the presented token and issues a new one in the same `family_id`. If an already-revoked token is presented, someone stole it: revoke the whole family.
- Passwords: `bcrypt` cost 12 (or argon2id). Minimum 8 chars; reject top-common passwords.

### 3.3 Login flow
```
1. rate-limit key rl:login:{ip}:{email}; >=5 failures -> 429 for 15 min
2. SELECT id, organization_id, password_hash, status, must_change_password
   FROM users WHERE (email = ? OR phone = ?) AND deleted_at IS NULL
3. constant-time compare (run bcrypt on a dummy hash if user not found)
4. status must be 'active'; org status must be 'active' (skip org check for system_admin)
5. host org (tenancy.Resolve) must equal users.organization_id, unless system_admin
6. issue tokens, INSERT refresh_tokens, UPDATE users.last_login_at
7. clear rl:login key, write audit_logs ('login'), return tokens + /me payload
```
```go
func (s *Service) Login(ctx context.Context, in LoginInput, meta ClientMeta) (*Session, error) {
	key := fmt.Sprintf("rl:login:%s:%s", meta.IP, strings.ToLower(in.Identifier))
	if n, _ := s.rdb.Get(ctx, key).Int(); n >= 5 { return nil, httpx.ErrTooMany }

	u, err := s.repo.FindByIdentifier(ctx, in.Identifier)
	hash := dummyHash
	if err == nil { hash = u.PasswordHash }
	ok := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) == nil
	if err != nil || !ok || u.Status != "active" {
		s.rdb.Incr(ctx, key); s.rdb.Expire(ctx, key, 15*time.Minute)
		return nil, &httpx.Err{Status: 401, Code: "invalid_credentials", Message: "Invalid email/phone or password"}
	}
	s.rdb.Del(ctx, key)

	access, jti, exp := s.tokens.NewAccess(u.ID, u.OrganizationID)
	refresh, hash2 := s.tokens.NewRefresh()
	family := uuid.NewString()
	if err := s.repo.InsertRefresh(ctx, u.ID, family, hash2, meta, time.Now().Add(30*24*time.Hour)); err != nil { return nil, err }
	_ = s.repo.TouchLogin(ctx, u.ID)
	s.audit.Log(ctx, audit.Entry{OrgID: u.OrganizationID, UserID: &u.ID, Action: "login", Entity: "users", EntityID: &u.ID, IP: meta.IP})
	return &Session{Access: access, AccessExp: exp, Refresh: refresh, JTI: jti, MustChangePassword: u.MustChangePassword}, nil
}
```

### 3.4 Refresh rotation
```go
func (s *Service) Refresh(ctx context.Context, raw string, meta ClientMeta) (*Session, error) {
	h := sha256Hex(raw)
	rt, err := s.repo.FindRefresh(ctx, h) // SELECT ... FROM refresh_tokens WHERE token_hash = ?
	if err != nil { return nil, httpx.ErrUnauthorized }
	if rt.RevokedAt != nil {                       // reuse => theft
		_ = s.repo.RevokeFamily(ctx, rt.FamilyID)
		return nil, httpx.ErrUnauthorized
	}
	if time.Now().After(rt.ExpiresAt) { return nil, httpx.ErrUnauthorized }
	tx, _ := s.db.BeginTxx(ctx, nil)
	defer tx.Rollback()
	_ = s.repo.RevokeToken(ctx, tx, rt.ID)
	newRaw, newHash := s.tokens.NewRefresh()
	_ = s.repo.InsertRefreshTx(ctx, tx, rt.UserID, rt.FamilyID, newHash, meta, time.Now().Add(30*24*time.Hour))
	if err := tx.Commit(); err != nil { return nil, err }
	access, jti, exp := s.tokens.NewAccess(rt.UserID, rt.OrgID)
	return &Session{Access: access, AccessExp: exp, Refresh: newRaw, JTI: jti}, nil
}
```

### 3.5 Authenticate middleware (builds the Principal)
```go
func (a *Authn) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := a.tokens.ParseAccess(bearer(r))
		if err != nil { httpx.Fail(w, httpx.ErrUnauthorized); return }
		ctx := r.Context()
		if a.rdb.Exists(ctx, "jwt:deny:"+claims.ID).Val() == 1 { httpx.Fail(w, httpx.ErrUnauthorized); return }

		ver, _ := a.rdb.Get(ctx, fmt.Sprintf("authzver:%d", claims.OrgID)).Int()
		key := fmt.Sprintf("authz:%d:%d", claims.UserID, ver)
		p, err := Cached(ctx, a.rdb, key, 10*time.Minute, func() (*rbac.Principal, error) {
			return a.loader.Load(ctx, claims.UserID, claims.OrgID) // DB: roles, perms, scope
		})
		if err != nil { httpx.Fail(w, httpx.ErrUnauthorized); return }
		next.ServeHTTP(w, r.WithContext(rbac.WithPrincipal(ctx, p)))
	})
}
```

### 3.6 `GET /auth/me` - drives a dynamic frontend
```json
{ "data": {
  "user": { "id": 12, "first_name": "Wanjiku", "last_name": "Kamau", "phone": "+254712345678" },
  "organization": { "id": 1, "name": "Kamau Estates", "currency_code": "KES", "timezone": "Africa/Nairobi", "logo": "..." },
  "roles": ["property_manager"],
  "permissions": ["properties.view", "units.create", "leases.create", "..."],
  "scope": { "all_properties": false, "property_ids": [3, 7] },
  "must_change_password": false
} }
```
The frontend builds its menu from `permissions` (e.g. show "Leases" if `leases.view`), and hides buttons by permission. **The backend still enforces everything**; the menu is only convenience.

### 3.7 Gotchas
- Same response + timing for unknown user vs wrong password.
- Changing password / disabling a user: delete their `refresh_tokens`, `INCR authzver:{org}`, deny-list current jti.
- `system_admin` has `organization_id = NULL`; they operate on an org by sending `X-Org-ID`, which is honoured **only** for that role and audit-logged.

---

## 4. RBAC

### 4.1 Model
- `roles` (global built-ins have `organization_id NULL`; org custom roles carry the org id) -> `role_permissions` -> `permissions` (`module.action`). `user_roles` lets one person wear several hats; **permissions and scope are the union of all their roles**.
- **Scope** is derived from the role, not stored per user:

| Role | Scope loader query | Result |
|---|---|---|
| `system_admin`, `org_admin`, `accountant` | none | `AllProperties = true` |
| `landlord` | `SELECT p.id FROM properties p JOIN owners o ON o.id = p.owner_id WHERE o.user_id = ? AND p.deleted_at IS NULL` | owned properties |
| `property_manager`, `caretaker` | `SELECT property_id FROM property_assignments WHERE user_id = ? AND unassigned_at IS NULL` | assigned properties |
| `agent` | `SELECT pa.property_id FROM property_agents pa JOIN agents a ON a.id = pa.agent_id WHERE a.user_id = ?` | listed properties |
| `tenant` | `SELECT id FROM tenants WHERE user_id = ?` | `TenantID` (own data only) |
| `contractor` | `SELECT id FROM contractors WHERE user_id = ?` | `ContractorID` |

```go
func (l *Loader) Load(ctx context.Context, userID, orgID uint64) (*rbac.Principal, error) {
	p := &rbac.Principal{UserID: userID, OrgID: orgID, Perms: map[string]struct{}{}, Scope: rbac.Scope{OrgID: orgID}}
	roles, _ := l.repo.RolesForUser(ctx, userID)            // names + ids
	perms, _ := l.repo.PermissionsForRoles(ctx, roleIDs)    // SELECT DISTINCT p.code ...
	for _, c := range perms { p.Perms[c] = struct{}{} }
	for _, r := range roles {
		p.Roles = append(p.Roles, r.Name)
		switch r.Name {
		case "system_admin", "org_admin", "accountant": p.Scope.AllProperties = true
		case "landlord":                       p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, l.repo.OwnedProperties(ctx, userID)...)
		case "property_manager", "caretaker":  p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, l.repo.AssignedProperties(ctx, userID)...)
		case "agent":                          p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, l.repo.AgentProperties(ctx, userID)...)
		case "tenant":                         id := l.repo.TenantID(ctx, userID); p.Scope.TenantID = &id
		case "contractor":                     id := l.repo.ContractorID(ctx, userID); p.Scope.ContractorID = &id
		default: // custom role: scope = AssignedProperties unless role.flags says all
		}
	}
	return p, nil
}
```

### 4.2 Enforcing
```go
func Require(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if p == nil || !p.Can(perm) { httpx.Fail(w, httpx.ErrForbidden); return }
			next.ServeHTTP(w, r)
		})
	}
}
```
For single-resource routes the repo query includes the scope filter, so an out-of-scope id simply returns 404.

### 4.3 Access matrix (default seed, `000013`)
`R` = read, `W` = create/update, `D` = delete/terminate/void, `own` = only their own rows, `-` = none.

| Module | system/org admin | landlord | property_manager | caretaker | agent | accountant | tenant | contractor |
|---|---|---|---|---|---|---|---|---|
| Properties | RWD | R (owned) | R, update | R | R | R | - | - |
| Units | RWD | R | RW | R | R | R | - | - |
| Tenants | RWD | R | RW + sensitive | R | RW | R | own | - |
| Leases | RWD | R | RW, activate, terminate, renew | R | create, R | R | own R | - |
| Invoices | RWD | R | RW | - | - | RWD | own R | - |
| Payments / M-Pesa | RWD | R | record | - | - | RW + reconcile | own R | - |
| Utilities | RW | R | RW | record readings | - | R | - | - |
| Maintenance | RWD | R | RW + assign + close | RW | - | - | create + own R | assigned jobs RW |
| Users / Roles / Settings | RWD | - | settings R | - | - | - | - | - |
| Notifications | RW | R | send + R | - | - | - | own R | - |
| Dashboard / Reports | all | owned | assigned | assigned (ops) | assigned | financial | own | - |

### 4.4 What each role queries and gets back
**system_admin** - `GET /organizations`, create orgs and domains, impersonate via `X-Org-ID`. Gets cross-org counts only; never reads tenant PII by default.

**org_admin** - everything inside their org: all properties, users, roles, settings, reports. Returns full records.

**landlord (owner)** - read-only view of *their* portfolio.
```sql
-- GET /properties (scope injected: p.id IN (owned ids))
SELECT p.id, p.name, p.town, p.county,
       COUNT(u.id) AS units, SUM(u.status='occupied') AS occupied
FROM properties p LEFT JOIN units u ON u.property_id = p.id AND u.deleted_at IS NULL
WHERE p.organization_id = ? AND p.id IN (?) GROUP BY p.id;
```
Returns: occupancy, rent expected vs collected, arrears totals, maintenance spend. **Tenant PII is masked** (no national ID, income, KRA PIN: lacks `tenants.view_sensitive`).

**property_manager** - runs day to day for assigned buildings: units, leases (create/activate/terminate), record manual payments, assign maintenance. Gets full tenant records including sensitive fields.

**caretaker** - on-site: today's meter readings to enter, open maintenance, inspections, unit status. Returns tenant name, unit and phone only (no money, no ID, no income).

**agent** - sees vacant units of their properties, creates tenants and draft leases. Cannot activate a lease or see payments.

**accountant** - invoices, payments, unmatched M-Pesa queue, arrears and reports across all properties; no maintenance, no user admin.

**tenant** - strictly own data.
```sql
-- GET /invoices for a tenant (scope.TenantID injected)
SELECT i.id, i.invoice_number, i.due_date, i.total, i.amount_paid, i.balance, i.status
FROM invoices i
JOIN leases l ON l.id = i.lease_id
JOIN lease_tenants lt ON lt.lease_id = l.id
WHERE i.organization_id = ? AND lt.tenant_id = ? AND i.status <> 'draft'
ORDER BY i.due_date DESC;
```
Returns own lease summary, balance, payment history, pay-now info (paybill + account number), maintenance requests they raised. Never sees other tenants, landlord details beyond contact, or internal notes.

**contractor** - only `maintenance_jobs WHERE contractor_id = scope.ContractorID`: job details, unit address, access notes; updates status and notes; no tenant financials.

### 4.5 Field-level masking
One serializer per entity checks a permission, so DTOs differ by role without separate endpoints:
```go
func TenantView(t Tenant, p *rbac.Principal) TenantDTO {
	d := TenantDTO{ID: t.ID, Name: t.FirstName + " " + t.LastName, Phone: t.Phone}
	if p.Can("tenants.view_sensitive") {
		d.NationalID, d.KraPin, d.Income, d.Employer = &t.NationalID, &t.KraPin, &t.Income, &t.Employer
	}
	return d
}