package server

import (
	"context"
	"errors"
	"net/http"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// effectiveRoleForPipeline resolves the caller's role on a specific
// pipeline. The chain:
//
//   tenantRole (from auth context)  ↓
//   pipeline grant for this user    ↓
//   max(tenantRole, grantRole)      → returned
//
// A user with no tenant membership but a direct grant on this
// pipeline still gets the grant role — the "external collaborator"
// case in PipelineGrantsRepo.EffectiveRole.
//
// Returns ("", nil) when the caller has neither a tenant role nor a
// pipeline grant — handlers treat that as forbidden.
func (s *Server) effectiveRoleForPipeline(ctx context.Context, pipelineID string) (storage.Role, error) {
	claim, _ := auth.TenantFromContext(ctx)
	tenantRole := storage.Role(claim.Role)
	user, _ := auth.UserFromContext(ctx)
	var sub string
	if user != nil {
		sub = user.Sub
	}
	// No user sub → no grant lookup possible. Fall back to tenant role,
	// which is also what the existing RequireRole middleware checks
	// against. Handlers downstream of RequireRole have a valid user,
	// so this branch is mostly defensive.
	if sub == "" {
		return tenantRole, nil
	}
	if s.store == nil || s.store.PipelineGrants == nil {
		return tenantRole, nil
	}
	return s.store.PipelineGrants.EffectiveRole(ctx, pipelineID, sub, tenantRole)
}

// gatePipeline writes a 403 and returns false unless the caller's
// effective role on pipelineID meets minRole. Returns true on
// success so handlers can keep their happy-path flat:
//
//	if !s.gatePipeline(w, r, id, storage.RoleOperator) { return }
//
// Composes after RequireSession; intended to layer on top of the
// route-level RequireRole gate. A user who's a viewer on the tenant
// but holds an operator grant on this pipeline will pass; a user
// who's operator on the tenant but somehow ends up here for a
// pipeline they don't have grant access to (e.g. pipeline pinned
// to "grants only" — future feature) won't.
func (s *Server) gatePipeline(w http.ResponseWriter, r *http.Request, pipelineID string, minRole storage.Role) bool {
	got, err := s.effectiveRoleForPipeline(r.Context(), pipelineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !got.AtLeast(minRole) {
		writeError(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// ErrPipelineForbidden is the sentinel some helpers return when the
// caller fails the gatePipeline check inside a non-handler context
// (e.g. a service-layer function that wants to surface 403 to its
// caller without touching ResponseWriter).
var ErrPipelineForbidden = errors.New("pipeline access forbidden")
