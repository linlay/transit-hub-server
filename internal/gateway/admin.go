package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

type apiKeyResponse struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	KeyPrefix     string            `json:"key_prefix"`
	Source        string            `json:"source"`
	IssuerJTI     string            `json:"issuer_jti,omitempty"`
	IssuerName    string            `json:"issuer_name,omitempty"`
	Status        string            `json:"status"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	ForcedExpired bool              `json:"forced_expired"`
	RequestQuota  int64             `json:"request_quota"`
	TokenQuota    int64             `json:"token_quota"`
	AllowedModels []string          `json:"allowed_models"`
	RateLimits    []store.RateLimit `json:"rate_limits"`
	UsedRequests  int64             `json:"used_requests"`
	UsedTokens    int64             `json:"used_tokens"`
	LastUsedAt    *time.Time        `json:"last_used_at,omitempty"`
	DeletedAt     *time.Time        `json:"deleted_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type createAPIKeyRequest struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	ExpiresAt     *time.Time        `json:"expires_at"`
	RequestQuota  int64             `json:"request_quota"`
	TokenQuota    int64             `json:"token_quota"`
	AllowedModels []string          `json:"allowed_models"`
	RateLimits    []store.RateLimit `json:"rate_limits"`
}

type createAPIKeyResponse struct {
	apiKeyResponse
	Key string `json:"key"`
}

type patchAPIKeyRequest struct {
	Name          *string                `json:"name"`
	Description   *string                `json:"description"`
	Status        *string                `json:"status"`
	ExpiresAt     optionalTime           `json:"expires_at"`
	ForcedExpired *bool                  `json:"forced_expired"`
	RequestQuota  *int64                 `json:"request_quota"`
	TokenQuota    *int64                 `json:"token_quota"`
	AllowedModels optionalStringSlice    `json:"allowed_models"`
	RateLimits    optionalRateLimitSlice `json:"rate_limits"`
}

type batchAPIKeysRequest struct {
	Action    string   `json:"action"`
	IDs       []string `json:"ids"`
	IssuerJTI string   `json:"issuer_jti"`
}

type optionalTime struct {
	Set   bool
	Value *time.Time
}

type optionalStringSlice struct {
	Set   bool
	Value []string
}

type optionalRateLimitSlice struct {
	Set   bool
	Value []store.RateLimit
}

func (t *optionalTime) UnmarshalJSON(data []byte) error {
	t.Set = true
	if string(data) == "null" {
		t.Value = nil
		return nil
	}
	var parsed time.Time
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	t.Value = &parsed
	return nil
}

func (s *optionalStringSlice) UnmarshalJSON(data []byte) error {
	s.Set = true
	return json.Unmarshal(data, &s.Value)
}

func (s *optionalRateLimitSlice) UnmarshalJSON(data []byte) error {
	s.Set = true
	return json.Unmarshal(data, &s.Value)
}

func (g *Gateway) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			token = r.Header.Get("x-admin-token")
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(g.env.AdminToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if user, ok := g.authenticateAdminSession(r); ok {
			r = r.WithContext(withAdminUser(r.Context(), user))
			next.ServeHTTP(w, r)
			return
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing admin credentials")
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.env.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	allowedModels, err := g.validateAllowedModels(req.AllowedModels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := g.store.CreateAPIKey(r.Context(), store.CreateAPIKeyParams{
		Name:          req.Name,
		Description:   req.Description,
		ExpiresAt:     req.ExpiresAt,
		RequestQuota:  req.RequestQuota,
		TokenQuota:    req.TokenQuota,
		AllowedModels: allowedModels,
		RateLimits:    req.RateLimits,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := toAPIKeyResponse(created.APIKey)
	if name, ok, lookupErr := g.lookupIssuerName(r, created.APIKey.IssuerJTI); lookupErr == nil && ok {
		resp.IssuerName = name
	}
	writeJSON(w, http.StatusCreated, createAPIKeyResponse{
		apiKeyResponse: resp,
		Key:            created.PlainText,
	})
}

func (g *Gateway) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 50, 200)
	result, err := g.store.SearchAPIKeys(r.Context(), store.APIKeyListParams{
		Search:         r.URL.Query().Get("search"),
		Status:         r.URL.Query().Get("status"),
		Source:         r.URL.Query().Get("source"),
		IssuerJTI:      r.URL.Query().Get("issuer_jti"),
		IncludeDeleted: parseBoolQuery(r, "include_deleted"),
		Limit:          limit,
		Offset:         offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	issuerJTIs := make([]string, 0, len(result.Items))
	for _, key := range result.Items {
		if key.IssuerJTI != "" {
			issuerJTIs = append(issuerJTIs, key.IssuerJTI)
		}
	}
	issuerNames, err := g.store.IssuerNamesByJTI(r.Context(), issuerJTIs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]apiKeyResponse, 0, len(result.Items))
	for _, key := range result.Items {
		resp := toAPIKeyResponse(key)
		if name, ok := issuerNames[key.IssuerJTI]; ok {
			resp.IssuerName = name
		}
		items = append(items, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  result.Total,
		"limit":  result.Limit,
		"offset": result.Offset,
	})
}

func (g *Gateway) getAPIKey(w http.ResponseWriter, r *http.Request) {
	key, err := g.store.GetAPIKey(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := toAPIKeyResponse(key)
	if name, ok, lookupErr := g.lookupIssuerName(r, key.IssuerJTI); lookupErr == nil && ok {
		resp.IssuerName = name
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *Gateway) patchAPIKey(w http.ResponseWriter, r *http.Request) {
	var req patchAPIKeyRequest
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
	key, err := g.store.UpdateAPIKey(r.Context(), chi.URLParam(r, "id"), store.APIKeyPatch{
		Name:             req.Name,
		Description:      req.Description,
		Status:           req.Status,
		ExpiresAtSet:     req.ExpiresAt.Set,
		ExpiresAt:        req.ExpiresAt.Value,
		ForcedExpired:    req.ForcedExpired,
		RequestQuota:     req.RequestQuota,
		TokenQuota:       req.TokenQuota,
		AllowedModelsSet: req.AllowedModels.Set,
		AllowedModels:    allowedModels,
		RateLimitsSet:    req.RateLimits.Set,
		RateLimits:       req.RateLimits.Value,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := toAPIKeyResponse(key)
	if name, ok, lookupErr := g.lookupIssuerName(r, key.IssuerJTI); lookupErr == nil && ok {
		resp.IssuerName = name
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *Gateway) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	key, err := g.store.DeleteAPIKey(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := toAPIKeyResponse(key)
	if name, ok, lookupErr := g.lookupIssuerName(r, key.IssuerJTI); lookupErr == nil && ok {
		resp.IssuerName = name
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *Gateway) batchAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req batchAPIKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	result, err := g.store.BatchAPIKeys(r.Context(), store.APIKeyBatchParams{
		Action:    req.Action,
		IDs:       req.IDs,
		IssuerJTI: req.IssuerJTI,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (g *Gateway) listProviders(w http.ResponseWriter, r *http.Request) {
	overrides, err := g.store.ListRouteOverrides(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g.registry.Snapshot(overrides))
}

func (g *Gateway) reloadProviders(w http.ResponseWriter, r *http.Request) {
	providerConfigs, err := config.LoadProviderConfigs(g.env.ConfigDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := g.registry.Replace(providerConfigs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reloaded", "providers": len(providerConfigs)})
}

func (g *Gateway) setRoutePool(w http.ResponseWriter, r *http.Request) {
	publicModel := chi.URLParam(r, "public_model")
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Pool) != "" && !g.registry.HasPoolForModel(publicModel, req.Pool) {
		writeError(w, http.StatusBadRequest, "pool does not exist for model")
		return
	}
	if err := g.store.SetRouteOverride(r.Context(), publicModel, req.Pool); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_model": publicModel, "pool": req.Pool})
}

func (g *Gateway) clearRoutePool(w http.ResponseWriter, r *http.Request) {
	publicModel := chi.URLParam(r, "public_model")
	if err := g.store.SetRouteOverride(r.Context(), publicModel, ""); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_model": publicModel, "pool": ""})
}

func toAPIKeyResponse(key store.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:            key.ID,
		Name:          key.Name,
		Description:   key.Description,
		KeyPrefix:     key.KeyPrefix,
		Source:        key.Source,
		IssuerJTI:     key.IssuerJTI,
		Status:        key.Status,
		ExpiresAt:     key.ExpiresAt,
		ForcedExpired: key.ForcedExpired,
		RequestQuota:  key.RequestQuota,
		TokenQuota:    key.TokenQuota,
		AllowedModels: key.AllowedModels,
		RateLimits:    key.RateLimits,
		UsedRequests:  key.UsedRequests,
		UsedTokens:    key.UsedTokens,
		LastUsedAt:    key.LastUsedAt,
		DeletedAt:     key.DeletedAt,
		CreatedAt:     key.CreatedAt,
		UpdatedAt:     key.UpdatedAt,
	}
}

func (g *Gateway) lookupIssuerName(r *http.Request, jti string) (string, bool, error) {
	jti = strings.TrimSpace(jti)
	if jti == "" || g.store == nil {
		return "", false, nil
	}
	name, ok, err := g.store.IssuerNameByJTI(r.Context(), jti)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	return name, true, nil
}

func (g *Gateway) validateAllowedModels(models []string) ([]string, error) {
	normalized := store.NormalizeAllowedModels(models)
	if len(normalized) == 0 {
		return nil, errors.New("allowed_models must include at least one model")
	}
	knownModels := map[string]struct{}{}
	for _, model := range g.registry.PublicModels() {
		knownModels[model] = struct{}{}
	}
	unknown := []string{}
	for _, model := range normalized {
		if _, ok := knownModels[model]; !ok {
			unknown = append(unknown, model)
		}
	}
	if len(unknown) > 0 {
		return nil, errors.New("unknown allowed_models: " + strings.Join(unknown, ", "))
	}
	return normalized, nil
}

func pagination(r *http.Request, defaultLimit, maxLimit int) (int, int) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit <= 0 || limit > maxLimit {
		limit = defaultLimit
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return limit, offset
}

func parseBoolQuery(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}
