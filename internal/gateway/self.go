package gateway

import (
	"net/http"
	"time"

	"github.com/linlay/transit-hub/internal/store"
)

type selfAPIKeyResponse struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	KeyPrefix        string            `json:"key_prefix"`
	Source           string            `json:"source"`
	IssuerJTI        string            `json:"issuer_jti,omitempty"`
	Status           string            `json:"status"`
	ExpiresAt        *time.Time        `json:"expires_at,omitempty"`
	ForcedExpired    bool              `json:"forced_expired"`
	RequestQuota     int64             `json:"request_quota"`
	RequestRemaining int64             `json:"request_remaining"`
	RequestUnlimited bool              `json:"request_unlimited"`
	TokenQuota       int64             `json:"token_quota"`
	TokenRemaining   int64             `json:"token_remaining"`
	TokenUnlimited   bool              `json:"token_unlimited"`
	AllowedModels    []string          `json:"allowed_models"`
	RateLimits       []store.RateLimit `json:"rate_limits"`
	UsedRequests     int64             `json:"used_requests"`
	UsedTokens       int64             `json:"used_tokens"`
	LastUsedAt       *time.Time        `json:"last_used_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type selfLifetimeLimits struct {
	Requests         int64 `json:"requests"`
	RequestQuota     int64 `json:"request_quota"`
	RequestRemaining int64 `json:"request_remaining"`
	RequestUnlimited bool  `json:"request_unlimited"`
	Tokens           int64 `json:"tokens"`
	TokenQuota       int64 `json:"token_quota"`
	TokenRemaining   int64 `json:"token_remaining"`
	TokenUnlimited   bool  `json:"token_unlimited"`
}

type selfBalanceResponse struct {
	Currency  string                  `json:"currency"`
	CostMicro int64                   `json:"cost_micro"`
	Unlimited bool                    `json:"unlimited"`
	Items     []store.RateLimitStatus `json:"items"`
}

func (g *Gateway) currentAPIKey(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, selfAPIKeyFromStore(key))
}

func (g *Gateway) currentAPIKeyLimits(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	statuses, err := g.store.RateLimitStatuses(r.Context(), key.ID, key.RateLimits, time.Now().UTC(), g.rateLimitLocation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lifetime":         selfLifetimeFromKey(key),
		"rate_limit_usage": statuses,
	})
}

func (g *Gateway) currentAPIKeyUsage(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	summary, err := g.store.RequestLogSummary(r.Context(), store.RequestLogQuery{
		APIKeyID: key.ID,
		From:     from,
		To:       to,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, err := g.store.Traffic(r.Context(), store.TrafficQuery{
		APIKeyID: key.ID,
		From:     from,
		To:       to,
		Bucket:   r.URL.Query().Get("bucket"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": summary,
		"items":   items,
	})
}

func (g *Gateway) currentAPIKeyBalance(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	summary, err := g.store.RequestLogSummary(r.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	statuses, err := g.store.RateLimitStatuses(r.Context(), key.ID, key.RateLimits, time.Now().UTC(), g.rateLimitLocation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]store.RateLimitStatus, 0, len(statuses))
	for _, status := range statuses {
		if status.CostQuotaMicro > 0 {
			items = append(items, status)
		}
	}
	writeJSON(w, http.StatusOK, selfBalanceResponse{
		Currency:  g.configuredCurrency(),
		CostMicro: summary.CostMicro,
		Unlimited: len(items) == 0,
		Items:     items,
	})
}

func (g *Gateway) currentAPIKeyLogs(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r, 100, 500)
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.store.ListRequestLogs(r.Context(), store.RequestLogQuery{
		APIKeyID: key.ID,
		From:     from,
		To:       to,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (g *Gateway) currentAPIKeySessions(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r, 100, 500)
	result, err := g.store.ListAPISessions(r.Context(), store.APISessionQuery{
		APIKeyID:     key.ID,
		Search:       r.URL.Query().Get("search"),
		Source:       r.URL.Query().Get("source"),
		IncludeStale: parseBoolQuery(r, "include_stale"),
		ActiveWindow: g.env.SessionActiveWindow,
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (g *Gateway) currentAPIKeyPrices(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	prices, err := g.store.ListModelPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	allowed := g.allowedPriceKeys(key)
	items := make([]store.ModelPrice, 0, len(prices))
	for _, price := range prices {
		if _, ok := allowed[price.Protocol+"\x00"+price.PublicModel]; ok {
			items = append(items, price)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (g *Gateway) allowedPriceKeys(key store.APIKey) map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, route := range g.allowedRoutes(protocol, key) {
			allowed[route.Protocol+"\x00"+route.PublicModel] = struct{}{}
		}
	}
	return allowed
}

func selfAPIKeyFromStore(key store.APIKey) selfAPIKeyResponse {
	lifetime := selfLifetimeFromKey(key)
	return selfAPIKeyResponse{
		ID:               key.ID,
		Name:             key.Name,
		Description:      key.Description,
		KeyPrefix:        key.KeyPrefix,
		Source:           key.Source,
		IssuerJTI:        key.IssuerJTI,
		Status:           key.Status,
		ExpiresAt:        key.ExpiresAt,
		ForcedExpired:    key.ForcedExpired,
		RequestQuota:     key.RequestQuota,
		RequestRemaining: lifetime.RequestRemaining,
		RequestUnlimited: lifetime.RequestUnlimited,
		TokenQuota:       key.TokenQuota,
		TokenRemaining:   lifetime.TokenRemaining,
		TokenUnlimited:   lifetime.TokenUnlimited,
		AllowedModels:    key.AllowedModels,
		RateLimits:       key.RateLimits,
		UsedRequests:     key.UsedRequests,
		UsedTokens:       key.UsedTokens,
		LastUsedAt:       key.LastUsedAt,
		CreatedAt:        key.CreatedAt,
		UpdatedAt:        key.UpdatedAt,
	}
}

func selfLifetimeFromKey(key store.APIKey) selfLifetimeLimits {
	return selfLifetimeLimits{
		Requests:         key.UsedRequests,
		RequestQuota:     key.RequestQuota,
		RequestRemaining: quotaRemaining(key.RequestQuota, key.UsedRequests),
		RequestUnlimited: key.RequestQuota == 0,
		Tokens:           key.UsedTokens,
		TokenQuota:       key.TokenQuota,
		TokenRemaining:   quotaRemaining(key.TokenQuota, key.UsedTokens),
		TokenUnlimited:   key.TokenQuota == 0,
	}
}

func quotaRemaining(quota, used int64) int64 {
	if quota <= 0 {
		return 0
	}
	remaining := quota - used
	if remaining < 0 {
		return 0
	}
	return remaining
}
