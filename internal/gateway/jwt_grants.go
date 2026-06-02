package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linlay/transit-hub/internal/issuer"
	"github.com/linlay/transit-hub/internal/store"
)

type jwtGrantResponse struct {
	JTI            string     `json:"jti"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Status         string     `json:"status"`
	IssueQuota     int64      `json:"issue_quota"`
	IssuedCount    int64      `json:"issued_count"`
	IssueRemaining int64      `json:"issue_remaining"`
	IssueUnlimited bool       `json:"issue_unlimited"`
	RequestQuota   int64      `json:"request_quota"`
	TokenQuota     int64      `json:"token_quota"`
	AllowedModels  []string   `json:"allowed_models"`
	JWT            string     `json:"jwt,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastIssuedAt   *time.Time `json:"last_issued_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type createJWTGrantRequest struct {
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	IssueQuota    int64      `json:"issue_quota"`
	RequestQuota  *int64     `json:"request_quota"`
	TokenQuota    *int64     `json:"token_quota"`
	AllowedModels []string   `json:"allowed_models"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

type createJWTGrantResponse struct {
	jwtGrantResponse
	JWT string `json:"jwt"`
}

type patchJWTGrantRequest struct {
	Name          *string             `json:"name"`
	Description   *string             `json:"description"`
	Status        *string             `json:"status"`
	IssueQuota    *int64              `json:"issue_quota"`
	RequestQuota  *int64              `json:"request_quota"`
	TokenQuota    *int64              `json:"token_quota"`
	AllowedModels optionalStringSlice `json:"allowed_models"`
}

type applyAPIKeyRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (g *Gateway) createJWTGrant(w http.ResponseWriter, r *http.Request) {
	svc, ok := g.requireIssuer(w)
	if !ok {
		return
	}
	var req createJWTGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	allowedModels, err := g.validateAllowedModels(req.AllowedModels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	expiresAt := req.ExpiresAt
	if expiresAt == nil {
		defaultExpiresAt := now.Add(svc.DefaultJWTTTL())
		expiresAt = &defaultExpiresAt
	}
	requestQuota := svc.DefaultAPIKeyRequestQuota()
	if req.RequestQuota != nil {
		requestQuota = *req.RequestQuota
	}
	tokenQuota := svc.DefaultAPIKeyTokenQuota()
	if req.TokenQuota != nil {
		tokenQuota = *req.TokenQuota
	}
	jti := store.GenerateJTI()
	jwt, err := svc.SignGrant(jti, *expiresAt, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	grant, err := g.store.CreateJWTGrant(r.Context(), store.CreateJWTGrantParams{
		JTI:           jti,
		Name:          req.Name,
		Description:   req.Description,
		IssueQuota:    req.IssueQuota,
		RequestQuota:  requestQuota,
		TokenQuota:    tokenQuota,
		AllowedModels: allowedModels,
		JWT:           jwt,
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createJWTGrantResponse{
		jwtGrantResponse: toJWTGrantResponse(grant, false),
		JWT:              jwt,
	})
}

func (g *Gateway) listJWTGrants(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 50, 200)
	result, err := g.store.SearchJWTGrants(r.Context(), store.JWTGrantListParams{
		Search: r.URL.Query().Get("search"),
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]jwtGrantResponse, 0, len(result.Items))
	for _, grant := range result.Items {
		items = append(items, toJWTGrantResponse(grant, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  result.Total,
		"limit":  result.Limit,
		"offset": result.Offset,
	})
}

func (g *Gateway) getJWTGrant(w http.ResponseWriter, r *http.Request) {
	grant, err := g.store.GetJWTGrant(r.Context(), chi.URLParam(r, "jti"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "jwt grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toJWTGrantResponse(grant, true))
}

func (g *Gateway) patchJWTGrant(w http.ResponseWriter, r *http.Request) {
	var req patchJWTGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	allowedModels := req.AllowedModels.Value
	if req.AllowedModels.Set {
		var err error
		allowedModels, err = g.validateAllowedModels(req.AllowedModels.Value)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	grant, err := g.store.UpdateJWTGrant(r.Context(), chi.URLParam(r, "jti"), store.JWTGrantPatch{
		Name:             req.Name,
		Description:      req.Description,
		Status:           req.Status,
		IssueQuota:       req.IssueQuota,
		RequestQuota:     req.RequestQuota,
		TokenQuota:       req.TokenQuota,
		AllowedModelsSet: req.AllowedModels.Set,
		AllowedModels:    allowedModels,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "jwt grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toJWTGrantResponse(grant, false))
}

func (g *Gateway) deleteJWTGrant(w http.ResponseWriter, r *http.Request) {
	grant, err := g.store.DeleteJWTGrant(r.Context(), chi.URLParam(r, "jti"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "jwt grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toJWTGrantResponse(grant, false))
}

func (g *Gateway) applyAPIKey(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing jwt token")
		return
	}
	svc, ok := g.requireIssuer(w)
	if !ok {
		return
	}
	var req applyAPIKeyRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	now := time.Now().UTC()
	claims, err := svc.VerifyGrant(token, now)
	if errors.Is(err, issuer.ErrExpiredToken) {
		writeError(w, http.StatusUnauthorized, "jwt token expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid jwt token")
		return
	}
	created, err := g.store.IssueAPIKeyFromJWTGrant(r.Context(), claims.JTI, store.CreateAPIKeyParams{
		Name: req.Name,
	}, now)
	if errors.Is(err, store.ErrGrantQuotaExhausted) {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if errors.Is(err, store.ErrGrantNotFound) || errors.Is(err, store.ErrGrantInactive) || errors.Is(err, store.ErrGrantExpired) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createAPIKeyResponse{
		apiKeyResponse: toAPIKeyResponse(created.APIKey),
		Key:            created.PlainText,
	})
}

func (g *Gateway) requireIssuer(w http.ResponseWriter) (*issuer.Service, bool) {
	if g.issuer == nil {
		writeError(w, http.StatusServiceUnavailable, "jwt issuer is not configured")
		return nil, false
	}
	return g.issuer, true
}

func toJWTGrantResponse(grant store.JWTGrant, includeJWT bool) jwtGrantResponse {
	remaining := int64(0)
	unlimited := grant.IssueQuota == 0
	if grant.IssueQuota > 0 {
		remaining = grant.IssueQuota - grant.IssuedCount
		if remaining < 0 {
			remaining = 0
		}
	}
	response := jwtGrantResponse{
		JTI:            grant.JTI,
		Name:           grant.Name,
		Description:    grant.Description,
		Status:         grant.Status,
		IssueQuota:     grant.IssueQuota,
		IssuedCount:    grant.IssuedCount,
		IssueRemaining: remaining,
		IssueUnlimited: unlimited,
		RequestQuota:   grant.RequestQuota,
		TokenQuota:     grant.TokenQuota,
		AllowedModels:  grant.AllowedModels,
		ExpiresAt:      grant.ExpiresAt,
		LastIssuedAt:   grant.LastIssuedAt,
		CreatedAt:      grant.CreatedAt,
		UpdatedAt:      grant.UpdatedAt,
	}
	if includeJWT {
		response.JWT = grant.JWT
	}
	return response
}
