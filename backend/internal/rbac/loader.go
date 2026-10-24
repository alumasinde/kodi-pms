package rbac

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
)

type Loader struct {
	repo  *repository
	store cache.Store
	log   *slog.Logger
	ttl   time.Duration
}

func NewLoader(db *sqlx.DB, store cache.Store, log *slog.Logger) *Loader {
	return &Loader{repo: &repository{db: db}, store: store, log: log, ttl: 10 * time.Minute}
}

// Load builds the union of permissions and the derived data scope for a user
// in one organization. Results are cached per authorization version so a role
// change invalidates every user in the org at once.
func (l *Loader) Load(ctx context.Context, userID, orgID uint64) (*Principal, error) {
	if userID == 0 || orgID == 0 {
		return nil, httpx.ErrUnauthorized
	}
	ver := AuthzVersion(ctx, l.store, orgID)
	key := authzCacheKey(userID, orgID, ver)
	if raw, err := l.store.Get(ctx, key); err == nil {
		var p Principal
		if json.Unmarshal([]byte(raw), &p) == nil {
			return &p, nil
		}
	}

	u, err := l.repo.UserState(ctx, userID)
	if err != nil || u.Status != "active" {
		return nil, httpx.ErrUnauthorized
	}
	if u.OrganizationID.Valid && uint64(u.OrganizationID.Int64) != orgID {
		return nil, httpx.ErrUnauthorized
	}
	active, err := l.repo.OrgIsActive(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, httpx.ErrUnauthorized
	}

	roles, err := l.repo.RolesForUser(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	if !u.OrganizationID.Valid {
		systemAdmin := false
		for _, r := range roles {
			if r.Name == "system_admin" {
				systemAdmin = true
				break
			}
		}
		if !systemAdmin {
			return nil, httpx.ErrUnauthorized
		}
	}
	roleIDs := make([]uint64, 0, len(roles))
	p := &Principal{
		UserID:             userID,
		OrgID:              orgID,
		Perms:              make(map[string]struct{}),
		Scope:              Scope{OrgID: orgID},
		MustChangePassword: u.MustChangePassword,
	}
	for _, r := range roles {
		roleIDs = append(roleIDs, r.ID)
		p.Roles = append(p.Roles, r.Name)
	}

	codes, err := l.repo.PermissionCodes(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range codes {
		p.Perms[c] = struct{}{}
	}

	if err := l.loadScope(ctx, p, roles, userID, orgID); err != nil {
		return nil, err
	}
	p.Scope.PropertyIDs = dedupe(p.Scope.PropertyIDs)

	if b, err := json.Marshal(p); err == nil {
		if err := l.store.Set(ctx, key, string(b), l.ttl); err != nil {
			l.log.Debug("cache principal", "error", err)
		}
	}
	return p, nil
}

func (l *Loader) loadScope(ctx context.Context, p *Principal, roles []roleRow, userID, orgID uint64) error {
	var assignedLoaded bool
	addAssigned := func() error {
		if assignedLoaded {
			return nil
		}
		ids, err := l.repo.AssignedProperties(ctx, userID, orgID)
		if err != nil {
			return err
		}
		p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, ids...)
		assignedLoaded = true
		return nil
	}

	for _, r := range roles {
		switch r.Name {
		case "system_admin", "org_admin", "accountant":
			p.Scope.AllProperties = true
		case "landlord":
			ids, err := l.repo.OwnedProperties(ctx, userID, orgID)
			if err != nil {
				return err
			}
			p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, ids...)
		case "property_manager", "caretaker":
			if err := addAssigned(); err != nil {
				return err
			}
		case "agent":
			ids, err := l.repo.AgentProperties(ctx, userID, orgID)
			if err != nil {
				return err
			}
			p.Scope.PropertyIDs = append(p.Scope.PropertyIDs, ids...)
		case "tenant":
			id, err := l.repo.TenantID(ctx, userID, orgID)
			if err != nil {
				return err
			}
			p.Scope.TenantID = id
		case "contractor":
			id, err := l.repo.ContractorID(ctx, userID, orgID)
			if err != nil {
				return err
			}
			p.Scope.ContractorID = id
		default:
			if err := addAssigned(); err != nil {
				return err
			}
		}
	}
	return nil
}

func dedupe(ids []uint64) []uint64 {
	if len(ids) < 2 {
		return ids
	}
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
