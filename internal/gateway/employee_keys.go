package gateway

import (
	"encoding/json"
	"io"
	"net/http"
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
	key, err := g.store.ResolveEmployeeKey(r.Context(), store.EmployeeIdentity{
		Issuer: identity.Issuer, Claim: svc.Config.SubjectClaim, Subject: identity.Subject,
	}, store.CreateAPIKeyParams{
		Name:          identity.Name,
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

func (g *Gateway) employeeDefaultPolicy() store.EmployeePolicy {
	if g.employeeAuth == nil {
		return store.EmployeePolicy{AllowedModels: []string{}, RateLimits: []store.RateLimit{}}
	}
	cfg := g.employeeAuth.Config
	return store.EmployeePolicy{AllowedModels: cfg.AllowedModels, RequestQuota: cfg.RequestQuota,
		TokenQuota: cfg.TokenQuota, QuotaMicrocredits: cfg.QuotaMicrocredits, RateLimits: cfg.RateLimits}
}

type employeeAuthResponse struct {
	store.EmployeePolicy
	Enabled       bool   `json:"enabled"`
	SubjectClaim  string `json:"subject_claim"`
	NameClaim     string `json:"name_claim"`
	Issuer        string `json:"issuer,omitempty"`
	Audience      string `json:"audience,omitempty"`
	Algorithm     string `json:"algorithm,omitempty"`
	RequiredScope string `json:"required_scope,omitempty"`
}

func (g *Gateway) employeeAuthSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	policy, err := g.store.GetEmployeePolicy(r.Context(), g.employeeDefaultPolicy())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "employee defaults unavailable")
		return
	}
	response := employeeAuthResponse{EmployeePolicy: policy, SubjectClaim: "staffuserid", NameClaim: "name"}
	if g.employeeAuth != nil {
		cfg := g.employeeAuth.Config
		response.Enabled = true
		response.SubjectClaim, response.Issuer, response.Audience = cfg.SubjectClaim, cfg.Issuer, cfg.Audience
		response.NameClaim = cfg.NameClaim
		response.Algorithm, response.RequiredScope = cfg.Algorithm, cfg.RequiredScope
	}
	writeJSON(w, http.StatusOK, response)
}

func (g *Gateway) updateEmployeeAuthSettings(w http.ResponseWriter, r *http.Request) {
	var policy store.EmployeePolicy
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid employee defaults")
		return
	}
	models, err := g.validateAllowedModels(policy.AllowedModels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	policy.AllowedModels = models
	if policy.RequestQuota < 0 || policy.TokenQuota < 0 || policy.QuotaMicrocredits < 0 {
		writeError(w, http.StatusBadRequest, "quotas must be nonnegative")
		return
	}
	policy.RateLimits, err = store.NormalizeRateLimits(policy.RateLimits)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = g.store.SetEmployeePolicy(r.Context(), policy); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save employee defaults")
		return
	}
	g.employeeAuthSettings(w, r)
}
