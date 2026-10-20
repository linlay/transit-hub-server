package gateway

import (
	"net/http"
	"strconv"
	"time"

	"github.com/linlay/transit-hub/internal/store"
)

func (g *Gateway) lookupEmployeeKey(w http.ResponseWriter, r *http.Request, token string) (store.APIKey, bool) {
	svc := g.employeeAuth
	identity, err := svc.Verifier.Verify(token, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid employee access token")
		return store.APIKey{}, false
	}
	if _, err = g.validateAllowedModels(svc.Config.AllowedModels); err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee model policy unavailable")
		return store.APIKey{}, false
	}
	key, err := g.store.ResolveEmployeeKey(r.Context(), store.EmployeeIdentity{
		Issuer: identity.Issuer, Claim: svc.Config.SubjectClaim, Subject: identity.Subject,
	}, store.CreateAPIKeyParams{
		AllowedModels: svc.Config.AllowedModels, RequestQuota: svc.Config.RequestQuota,
		TokenQuota: svc.Config.TokenQuota, QuotaMicrocredits: svc.Config.QuotaMicrocredits,
		RateLimits: svc.Config.RateLimits,
	})
	if err != nil {
		g.logger.Printf("employee accounting identity lookup failed")
		writeError(w, http.StatusInternalServerError, "employee authentication unavailable")
		return store.APIKey{}, false
	}
	return key, true
}

func (g *Gateway) listEmployeeKeys(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	query.Set("source", "employee")
	r.URL.RawQuery = query.Encode()
	g.listAPIKeys(w, r)
}

func (g *Gateway) employeeAuthSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if g.employeeAuth == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "subject_claim": "staffuserid"})
		return
	}
	cfg := g.employeeAuth.Config
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "subject_claim": cfg.SubjectClaim, "issuer": cfg.Issuer,
		"audience": cfg.Audience, "algorithm": cfg.Algorithm, "required_scope": cfg.RequiredScope,
		"allowed_models": cfg.AllowedModels, "request_quota": cfg.RequestQuota,
		"token_quota": cfg.TokenQuota, "quota_microcredits": strconv.FormatInt(cfg.QuotaMicrocredits, 10),
		"rate_limits": cfg.RateLimits,
	})
}
