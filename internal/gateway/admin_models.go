package gateway

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/provider"
)

type adminModelResponse struct {
	Protocol        string `json:"protocol"`
	Type            string `json:"type"`
	PublicModel     string `json:"public_model"`
	UpstreamModel   string `json:"upstream_model"`
	DisplayName     string `json:"display_name"`
	OwnedBy         string `json:"owned_by"`
	CreatedAt       string `json:"created_at"`
	Provider        string `json:"provider"`
	ProviderBaseURL string `json:"provider_base_url"`
	DefaultPool     string `json:"default_pool"`
	ConfiguredPool  string `json:"configured_pool"`
	OverridePool    string `json:"override_pool,omitempty"`
	OverrideValid   bool   `json:"override_valid"`
	EffectivePool   string `json:"effective_pool"`
	EndpointKey     string `json:"endpoint_key,omitempty"`
	GatewayPath     string `json:"gateway_path,omitempty"`
	UpstreamPath    string `json:"upstream_path,omitempty"`
	UpstreamURL     string `json:"upstream_url,omitempty"`
}

type adminModelListResponse struct {
	Items []adminModelResponse `json:"items"`
}

func (g *Gateway) listAdminModels(w http.ResponseWriter, r *http.Request) {
	overrides, err := g.store.ListRouteOverrides(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	routes := g.registry.PublicRoutes("")
	models := make([]adminModelResponse, 0, len(routes))
	for _, route := range routes {
		models = append(models, adminModelFromRoute(route, overrides[route.PublicModel]))
	}
	writeJSON(w, http.StatusOK, adminModelListResponse{Items: models})
}

func (g *Gateway) getAdminModel(w http.ResponseWriter, r *http.Request) {
	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	publicModel := strings.TrimSpace(r.URL.Query().Get("public_model"))
	if protocol == "" || publicModel == "" {
		writeError(w, http.StatusBadRequest, "protocol and public_model are required")
		return
	}

	route, ok := g.registry.Resolve(protocol, publicModel)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	override, exists, err := g.store.GetRouteOverride(r.Context(), publicModel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		override = ""
	}
	writeJSON(w, http.StatusOK, adminModelFromRoute(route, override))
}

func adminModelFromRoute(route provider.Route, overridePool string) adminModelResponse {
	endpointKey, gatewayPath := adminModelEndpoint(route)
	upstreamPath := ""
	upstreamURL := ""
	providerBaseURL := ""
	if route.Provider != nil && route.Provider.BaseURL != nil {
		baseURL := sanitizedURL(route.Provider.BaseURL)
		providerBaseURL = baseURL.String()
		if endpointKey != "" {
			upstreamPath = route.EndpointPath(endpointKey, gatewayPath)
			upstreamURL = joinUpstreamURL(baseURL, upstreamPath, "")
		}
	}

	overridePool = strings.TrimSpace(overridePool)
	overrideValid := true
	effectivePool := route.PoolName
	if overridePool != "" {
		if _, ok := route.Provider.Pools[overridePool]; ok {
			effectivePool = overridePool
		} else {
			overrideValid = false
			effectivePool = ""
		}
	}

	return adminModelResponse{
		Protocol:        route.Protocol,
		Type:            route.Type,
		PublicModel:     route.PublicModel,
		UpstreamModel:   route.UpstreamModel,
		DisplayName:     route.DisplayName,
		OwnedBy:         route.OwnedBy,
		CreatedAt:       route.CreatedAt.UTC().Format(time.RFC3339),
		Provider:        route.ProviderName,
		ProviderBaseURL: providerBaseURL,
		DefaultPool:     route.Provider.DefaultPool,
		ConfiguredPool:  route.PoolName,
		OverridePool:    overridePool,
		OverrideValid:   overrideValid,
		EffectivePool:   effectivePool,
		EndpointKey:     endpointKey,
		GatewayPath:     gatewayPath,
		UpstreamPath:    upstreamPath,
		UpstreamURL:     upstreamURL,
	}
}

func adminModelEndpoint(route provider.Route) (string, string) {
	if route.Protocol == "anthropic" {
		if route.Type == config.ModelTypeChat {
			return "anthropic_messages", "/v1/messages"
		}
		return "", ""
	}
	if route.Protocol != "openai" {
		return "", ""
	}
	switch route.Type {
	case config.ModelTypeChat:
		return "openai_chat_completions", "/v1/chat/completions"
	case config.ModelTypeEmbedding:
		return "openai_embeddings", "/v1/embeddings"
	case config.ModelTypeImageGeneration:
		return "openai_image_generations", "/v1/images/generations"
	default:
		return "", ""
	}
}

func sanitizedURL(source *url.URL) *url.URL {
	clean := *source
	clean.User = nil
	clean.RawQuery = ""
	clean.ForceQuery = false
	clean.Fragment = ""
	return &clean
}
