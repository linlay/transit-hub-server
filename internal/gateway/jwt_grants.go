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
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastIssuedAt   *time.Time `json:"last_issued_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type createJWTGrantRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	IssueQuota  int64      `json:"issue_quota"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type createJWTGrantResponse struct {
	jwtGrantResponse
	JWT string `json:"jwt"`
}

type patchJWTGrantRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	IssueQuota  *int64  `json:"issue_quota"`
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
	now := time.Now().UTC()
	expiresAt := req.ExpiresAt
	if expiresAt == nil {
		defaultExpiresAt := now.Add(svc.DefaultJWTTTL())
		expiresAt = &defaultExpiresAt
	}
	jti := store.GenerateJTI()
	jwt, err := svc.SignGrant(jti, *expiresAt, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	grant, err := g.store.CreateJWTGrant(r.Context(), store.CreateJWTGrantParams{
		JTI:         jti,
		Name:        req.Name,
		Description: req.Description,
		IssueQuota:  req.IssueQuota,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createJWTGrantResponse{
		jwtGrantResponse: toJWTGrantResponse(grant),
		JWT:              jwt,
	})
}

func (g *Gateway) listJWTGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := g.store.ListJWTGrants(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]jwtGrantResponse, 0, len(grants))
	for _, grant := range grants {
		items = append(items, toJWTGrantResponse(grant))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  len(items),
		"limit":  len(items),
		"offset": 0,
	})
}

func (g *Gateway) patchJWTGrant(w http.ResponseWriter, r *http.Request) {
	var req patchJWTGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	grant, err := g.store.UpdateJWTGrant(r.Context(), chi.URLParam(r, "jti"), store.JWTGrantPatch{
		Name:        req.Name,
		Description: req.Description,
		Status:      req.Status,
		IssueQuota:  req.IssueQuota,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "jwt grant not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toJWTGrantResponse(grant))
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
		Name:         req.Name,
		Description:  req.Description,
		RequestQuota: svc.DefaultAPIKeyRequestQuota(),
		TokenQuota:   svc.DefaultAPIKeyTokenQuota(),
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

func toJWTGrantResponse(grant store.JWTGrant) jwtGrantResponse {
	remaining := int64(0)
	unlimited := grant.IssueQuota == 0
	if grant.IssueQuota > 0 {
		remaining = grant.IssueQuota - grant.IssuedCount
		if remaining < 0 {
			remaining = 0
		}
	}
	return jwtGrantResponse{
		JTI:            grant.JTI,
		Name:           grant.Name,
		Description:    grant.Description,
		Status:         grant.Status,
		IssueQuota:     grant.IssueQuota,
		IssuedCount:    grant.IssuedCount,
		IssueRemaining: remaining,
		IssueUnlimited: unlimited,
		ExpiresAt:      grant.ExpiresAt,
		LastIssuedAt:   grant.LastIssuedAt,
		CreatedAt:      grant.CreatedAt,
		UpdatedAt:      grant.UpdatedAt,
	}
}
