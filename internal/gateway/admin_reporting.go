package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linlay/transit-hub/internal/store"
)

type modelPriceRequest struct {
	Protocol                             string `json:"protocol"`
	PublicModel                          string `json:"public_model"`
	InputCostMicroUSDPer1MTokens         int64  `json:"input_cost_microusd_per_1m_tokens"`
	InputCacheHitCostMicroUSDPer1MTokens *int64 `json:"input_cache_hit_cost_microusd_per_1m_tokens"`
	OutputCostMicroUSDPer1MTokens        int64  `json:"output_cost_microusd_per_1m_tokens"`
	Currency                             string `json:"currency"`
}

func (g *Gateway) overview(w http.ResponseWriter, r *http.Request) {
	overview, err := g.store.Overview(r.Context(), g.env.SessionActiveWindow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (g *Gateway) traffic(w http.ResponseWriter, r *http.Request) {
	query, err := trafficQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	buckets, err := g.store.Traffic(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": buckets})
}

func (g *Gateway) apiKeyUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := g.store.APIKeyUsage(r.Context(), chi.URLParam(r, "id"), g.env.SessionActiveWindow)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (g *Gateway) apiKeyLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 100, 500)
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.store.ListRequestLogs(r.Context(), store.RequestLogQuery{
		APIKeyID: chi.URLParam(r, "id"),
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

func (g *Gateway) requestLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 100, 500)
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.store.ListRequestLogs(r.Context(), store.RequestLogQuery{
		APIKeyID: r.URL.Query().Get("api_key_id"),
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

func (g *Gateway) providerUsage(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := g.store.ProviderUsage(r.Context(), store.ProviderUsageQuery{
		From: from,
		To:   to,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	accountItems, err := g.store.ProviderAccountUsage(r.Context(), store.ProviderUsageQuery{
		From: from,
		To:   to,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "account_items": accountItems})
}

func (g *Gateway) sessions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 100, 500)
	result, err := g.store.ListAPISessions(r.Context(), store.APISessionQuery{
		APIKeyID:     r.URL.Query().Get("api_key_id"),
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

func (g *Gateway) apiKeySessions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 100, 500)
	result, err := g.store.ListAPISessions(r.Context(), store.APISessionQuery{
		APIKeyID:     chi.URLParam(r, "id"),
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

func (g *Gateway) listModelPrices(w http.ResponseWriter, r *http.Request) {
	prices, err := g.store.ListModelPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": prices})
}

func (g *Gateway) createModelPrice(w http.ResponseWriter, r *http.Request) {
	var req modelPriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	price, err := g.store.UpsertModelPrice(r.Context(), store.ModelPriceParams{
		Protocol:                             req.Protocol,
		PublicModel:                          req.PublicModel,
		InputCostMicroUSDPer1MTokens:         req.InputCostMicroUSDPer1MTokens,
		InputCacheHitCostMicroUSDPer1MTokens: req.InputCacheHitCostMicroUSDPer1MTokens,
		OutputCostMicroUSDPer1MTokens:        req.OutputCostMicroUSDPer1MTokens,
		Currency:                             req.Currency,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, price)
}

func (g *Gateway) patchModelPrice(w http.ResponseWriter, r *http.Request) {
	var req modelPriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	price, err := g.store.UpdateModelPrice(r.Context(), chi.URLParam(r, "id"), store.ModelPriceParams{
		Protocol:                             req.Protocol,
		PublicModel:                          req.PublicModel,
		InputCostMicroUSDPer1MTokens:         req.InputCostMicroUSDPer1MTokens,
		InputCacheHitCostMicroUSDPer1MTokens: req.InputCacheHitCostMicroUSDPer1MTokens,
		OutputCostMicroUSDPer1MTokens:        req.OutputCostMicroUSDPer1MTokens,
		Currency:                             req.Currency,
	})
	if errors.Is(err, store.ErrPriceNotFound) {
		writeError(w, http.StatusNotFound, "model price not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, price)
}

func (g *Gateway) deleteModelPrice(w http.ResponseWriter, r *http.Request) {
	if err := g.store.DeleteModelPrice(r.Context(), chi.URLParam(r, "id")); errors.Is(err, store.ErrPriceNotFound) {
		writeError(w, http.StatusNotFound, "model price not found")
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

func trafficQueryFromRequest(r *http.Request) (store.TrafficQuery, error) {
	from, to, err := parseTimeRange(r)
	if err != nil {
		return store.TrafficQuery{}, err
	}
	return store.TrafficQuery{
		APIKeyID: r.URL.Query().Get("api_key_id"),
		From:     from,
		To:       to,
		Bucket:   r.URL.Query().Get("bucket"),
	}, nil
}

func parseTimeRange(r *http.Request) (*time.Time, *time.Time, error) {
	var from *time.Time
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, err
		}
		from = &parsed
	}
	var to *time.Time
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, err
		}
		to = &parsed
	}
	return from, to, nil
}
