package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
)

const (
	defaultAnthropicModelLimit = 20
	maxAnthropicModelLimit     = 100
)

type openAIModelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelListResponse struct {
	Object string                `json:"object"`
	Data   []openAIModelResponse `json:"data"`
}

type anthropicModelResponse struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type anthropicModelListResponse struct {
	Data    []anthropicModelResponse `json:"data"`
	FirstID string                   `json:"first_id,omitempty"`
	LastID  string                   `json:"last_id,omitempty"`
	HasMore bool                     `json:"has_more"`
}

func (g *Gateway) listOpenAIModels(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	routes := g.allowedRoutes("openai", key)
	models := make([]openAIModelResponse, 0, len(routes))
	for _, route := range routes {
		models = append(models, openAIModelFromRoute(route))
	}
	writeJSON(w, http.StatusOK, openAIModelListResponse{
		Object: "list",
		Data:   models,
	})
}

func (g *Gateway) retrieveOpenAIModel(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	route, ok := g.allowedRoute("openai", chi.URLParam(r, "model_id"), key)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	writeJSON(w, http.StatusOK, openAIModelFromRoute(route))
}

func (g *Gateway) listAnthropicModels(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	limit, err := anthropicModelLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	routes := g.allowedRoutes("anthropic", key)
	window, err := anthropicModelWindow(routes, r.URL.Query().Get("after_id"), r.URL.Query().Get("before_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hasMore := len(window) > limit
	if hasMore {
		window = window[:limit]
	}
	models := make([]anthropicModelResponse, 0, len(window))
	for _, route := range window {
		models = append(models, anthropicModelFromRoute(route))
	}
	response := anthropicModelListResponse{
		Data:    models,
		HasMore: hasMore,
	}
	if len(models) > 0 {
		response.FirstID = models[0].ID
		response.LastID = models[len(models)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (g *Gateway) retrieveAnthropicModel(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authenticatePublicMetadataKey(w, r)
	if !ok {
		return
	}
	route, ok := g.allowedRoute("anthropic", chi.URLParam(r, "model_id"), key)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	writeJSON(w, http.StatusOK, anthropicModelFromRoute(route))
}

func (g *Gateway) allowedRoutes(protocol string, key store.APIKey) []provider.Route {
	routes := g.registry.PublicRoutes(protocol)
	allowed := make([]provider.Route, 0, len(routes))
	for _, route := range routes {
		if store.APIKeyAllowsModel(key, route.PublicModel) {
			allowed = append(allowed, route)
		}
	}
	return allowed
}

func (g *Gateway) allowedRoute(protocol, modelID string, key store.APIKey) (provider.Route, bool) {
	modelID = strings.TrimSpace(modelID)
	if !store.APIKeyAllowsModel(key, modelID) {
		return provider.Route{}, false
	}
	return g.registry.Resolve(protocol, modelID)
}

func openAIModelFromRoute(route provider.Route) openAIModelResponse {
	return openAIModelResponse{
		ID:      route.PublicModel,
		Object:  "model",
		Created: route.CreatedAt.Unix(),
		OwnedBy: route.OwnedBy,
	}
}

func anthropicModelFromRoute(route provider.Route) anthropicModelResponse {
	return anthropicModelResponse{
		ID:          route.PublicModel,
		Type:        "model",
		DisplayName: route.DisplayName,
		CreatedAt:   route.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func anthropicModelLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultAnthropicModelLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, errors.New("limit must be a positive integer")
	}
	if limit > maxAnthropicModelLimit {
		limit = maxAnthropicModelLimit
	}
	return limit, nil
}

func anthropicModelWindow(routes []provider.Route, afterID, beforeID string) ([]provider.Route, error) {
	start := 0
	end := len(routes)
	afterID = strings.TrimSpace(afterID)
	beforeID = strings.TrimSpace(beforeID)
	if afterID != "" {
		index := routeIndex(routes, afterID)
		if index < 0 {
			return nil, errors.New("after_id model not found")
		}
		start = index + 1
	}
	if beforeID != "" {
		index := routeIndex(routes, beforeID)
		if index < 0 {
			return nil, errors.New("before_id model not found")
		}
		end = index
	}
	if start > end {
		start = end
	}
	return routes[start:end], nil
}

func routeIndex(routes []provider.Route, modelID string) int {
	for i, route := range routes {
		if route.PublicModel == modelID {
			return i
		}
	}
	return -1
}
