package auth

import (
	"net/http"
)

// DefaultTenantID mirrors storage.DefaultTenantID. Duplicated here to
// avoid an import cycle (auth → storage → auth). Keep in sync.
const DefaultTenantID = "00000000-0000-0000-0000-000000000000"

// TenantResolver is implemented by something that, given a verified
// user and the request, returns the active tenant claim. The concrete
// implementation lives in a higher layer (it needs storage access).
// When the resolver returns ok=false, the request is treated as having
// no active tenant — downstream tenant-required handlers return 403.
//
// Set via Service.SetTenantResolver during server boot. A nil resolver
// falls back to a single-tenant model (every authed user is an
// implicit owner of the default tenant) — that's the behaviour the
// codebase had before multi-tenancy landed and lets pre-16b tests
// keep working unchanged.
type TenantResolver interface {
	Resolve(r *http.Request, user any) (TenantClaim, bool)
}

// SetTenantResolver installs the resolver used by RequireSession.
func (s *Service) SetTenantResolver(tr TenantResolver) { s.tenantResolver = tr }

// RequireSession gates a handler behind a valid session cookie. On
// success the user and the active tenant are written into the context.
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookieName)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		user, err := s.Validate(c.Value)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		ctx := WithUser(r.Context(), user)

		// Resolve the active tenant. With no resolver installed, fall
		// back to the legacy "everyone is owner of the default tenant"
		// shape so single-tenant deployments and the existing test
		// suite keep working unchanged.
		var claim TenantClaim
		if s.tenantResolver != nil {
			if c, ok := s.tenantResolver.Resolve(r, user); ok {
				claim = c
			}
		}
		if claim.TenantID == "" {
			claim = TenantClaim{TenantID: DefaultTenantID, Role: "owner"}
		}
		ctx = WithTenant(ctx, claim)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole rejects requests whose user does not hold role. Must be
// composed after RequireSession.
func (s *Service) RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok || user == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			if !user.HasRole(role) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
