package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bodaay/simpleauth-go"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// tenantResolver is the production TenantResolver. Given a verified
// SimpleAuth user, it looks up their memberships in storage and picks
// the active tenant for the request.
//
// Selection precedence:
//  1. X-Tenant-Id header — for API tokens / SDK callers
//  2. mqc_active_tenant cookie — for browser UI with a tenant switcher
//  3. The user's lowest-numbered (alphabetically first) tenant id
//
// If the user has NO memberships, the resolver returns ok=false and
// the request lands at every storage check as "wrong tenant" → 404
// (existence-leak-proof). The login handler is responsible for
// auto-provisioning a membership when SimpleAuth says the user is the
// bootstrap admin.
//
// All lookups go through storage; no caching here. At ~1 SQL roundtrip
// per request the overhead is negligible at our target scale, and the
// no-cache rule means a role change is effective on the next request.
type tenantResolver struct {
	store  *storage.Store
	logger *slog.Logger
}

func newTenantResolver(store *storage.Store, logger *slog.Logger) *tenantResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &tenantResolver{store: store, logger: logger.With("component", "auth.tenantResolver")}
}

// Resolve satisfies auth.TenantResolver.
func (tr *tenantResolver) Resolve(r *http.Request, userIface any) (auth.TenantClaim, bool) {
	user, ok := userIface.(*simpleauth.User)
	if !ok || user == nil {
		return auth.TenantClaim{}, false
	}

	// One-shot bootstrap adoption: the very first time a real JWT lands
	// for a user that matches the bootstrap row, swap the row over so
	// subsequent lookups find them directly by sub.
	username := user.PreferredUsername
	if username == "" {
		username = user.Name
	}

	ctx := r.Context()
	memberships, err := tr.store.Memberships.ListByUser(ctx, user.Sub, strings.ToLower(username))
	if err != nil {
		tr.logger.Warn("memberships lookup failed", "sub", user.Sub, "err", err)
		return auth.TenantClaim{}, false
	}
	if len(memberships) == 0 {
		return auth.TenantClaim{}, false
	}

	requested := requestedTenant(r)

	// System-admin escalation: a user with the owner role on the
	// default tenant gets implicit owner on any tenant they explicitly
	// request. Lets the on-prem operator manage every tenant from one
	// account without adding themselves as owner to each one.
	if requested != "" && isDefaultOwner(memberships) {
		// Prefer a real membership when it exists.
		for _, m := range memberships {
			if m.TenantID == requested {
				return auth.TenantClaim{TenantID: m.TenantID, Role: string(m.Role)}, true
			}
		}
		// Verify the tenant actually exists before granting — refuse
		// to synthesize a role for a typo.
		if _, err := tr.store.Tenants.Get(ctx, requested); err == nil {
			return auth.TenantClaim{TenantID: requested, Role: string(storage.RoleOwner)}, true
		}
	}

	chosen := pickMembership(memberships, requested)
	return auth.TenantClaim{
		TenantID: chosen.TenantID,
		Role:     string(chosen.Role),
	}, true
}

// isDefaultOwner returns true when the user holds the owner role on the
// seeded default tenant — i.e. they are the on-prem system administrator.
func isDefaultOwner(ms []*storage.Membership) bool {
	for _, m := range ms {
		if m.TenantID == storage.DefaultTenantID && m.Role == storage.RoleOwner {
			return true
		}
	}
	return false
}

// requestedTenant returns the tenant the caller explicitly asked for,
// or "" if none. Header beats cookie.
func requestedTenant(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("X-Tenant-Id")); h != "" {
		return h
	}
	if c, err := r.Cookie("mqc_active_tenant"); err == nil {
		return c.Value
	}
	return ""
}

func pickMembership(ms []*storage.Membership, requested string) *storage.Membership {
	if requested != "" {
		for _, m := range ms {
			if m.TenantID == requested {
				return m
			}
		}
		// Requested tenant not in the user's set — fall through to the
		// default pick rather than returning 403. Asking the wrong
		// question shouldn't deny access entirely; the storage layer
		// will refuse to expose anything cross-tenant anyway.
	}
	// memberships come back ordered by tenant_id from ListByUser, so
	// the first element is deterministic.
	return ms[0]
}

// withMembershipPromotion promotes a bootstrap:<username> membership
// row to the user's real sub on first successful login. Safe to call
// every login; subsequent calls are no-ops.
func (tr *tenantResolver) withMembershipPromotion(ctx context.Context, user *simpleauth.User) {
	if user == nil || user.Sub == "" {
		return
	}
	username := user.PreferredUsername
	if username == "" {
		username = user.Name
	}
	if username == "" {
		return
	}
	// ListByUser already triggers adoption when no rows match the sub.
	// We don't care about the result, just the side effect.
	_, _ = tr.store.Memberships.ListByUser(ctx, user.Sub, strings.ToLower(username))
}
