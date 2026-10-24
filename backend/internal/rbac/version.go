package rbac

import (
	"context"
	"fmt"
	"strconv"

	"kodipms/internal/platform/cache"
)

func AuthzVersion(ctx context.Context, store cache.Store, orgID uint64) int64 {
	raw, err := store.Get(ctx, fmt.Sprintf("authzver:%d", orgID))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// BumpAuthzVersion makes every cached principal for the organization
// unreachable. Call after changing roles, role permissions or user roles.
func BumpAuthzVersion(ctx context.Context, store cache.Store, orgID uint64) error {
	_, err := store.Incr(ctx, fmt.Sprintf("authzver:%d", orgID))
	return err
}

// UserAuthzVersion is separate from the organization version so a password
// reset only invalidates that user's cached principal, not every user in the
// organization.
func UserAuthzVersion(ctx context.Context, store cache.Store, userID uint64) int64 {
	raw, err := store.Get(ctx, fmt.Sprintf("authzver:user:%d", userID))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func BumpUserAuthzVersion(ctx context.Context, store cache.Store, userID uint64) error {
	_, err := store.Incr(ctx, fmt.Sprintf("authzver:user:%d", userID))
	return err
}

func authzCacheKey(userID, orgID uint64, orgVersion, userVersion int64) string {
	return fmt.Sprintf("authz:%d:%d:%d:%d", userID, orgID, orgVersion, userVersion)
}
