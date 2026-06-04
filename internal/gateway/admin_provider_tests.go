package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/provider"
)

const connectivityErrorSampleLimit = 4 * 1024

type providerConnectivityTestRequest struct {
	Provider    string `json:"provider"`
	PublicModel string `json:"public_model"`
	Pool        string `json:"pool"`
	Account     string `json:"account"`
}

type providerConnectivityTestResponse struct {
	OK            bool      `json:"ok"`
	Provider      string    `json:"provider"`
	Protocol      string    `json:"protocol"`
	PublicModel   string    `json:"public_model"`
	UpstreamModel string    `json:"upstream_model"`
	Pool          string    `json:"pool"`
	Account       string    `json:"account"`
	Endpoint      string    `json:"endpoint"`
	StatusCode    int       `json:"status_code"`
	LatencyMS     int64     `json:"latency_ms"`
	Error         string    `json:"error,omitempty"`
	TestedAt      time.Time `json:"tested_at"`
}

func (g *Gateway) testProviderConnectivity(w http.ResponseWriter, r *http.Request) {
	var req providerConnectivityTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.PublicModel = strings.TrimSpace(req.PublicModel)
	req.Pool = strings.TrimSpace(req.Pool)
	req.Account = strings.TrimSpace(req.Account)
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	route, account, err := g.registry.ResolveConnectivityTarget(provider.ConnectivityTarget{
		ProviderName: req.Provider,
		PublicModel:  req.PublicModel,
		PoolName:     req.Pool,
		AccountName:  req.Account,
	})
	if err != nil {
		writeConnectivityTargetError(w, err)
		return
	}

	body, endpointKey, fallbackPath, err := connectivityProbeBody(route)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	upstreamReq, err := g.buildConnectivityRequest(r, route, account, endpointKey, fallbackPath, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	started := time.Now()
	result := providerConnectivityTestResponse{
		Provider:      route.ProviderName,
		Protocol:      route.Protocol,
		PublicModel:   route.PublicModel,
		UpstreamModel: route.UpstreamModel,
		Pool:          route.PoolName,
		Account:       account.Name,
		Endpoint:      upstreamReq.URL.String(),
		TestedAt:      started.UTC(),
	}

	resp, err := g.client.Do(upstreamReq)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("upstream request failed: %v", err)
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !result.OK {
		sample, readErr := io.ReadAll(io.LimitReader(resp.Body, connectivityErrorSampleLimit))
		if readErr != nil {
			result.Error = readErr.Error()
		} else {
			result.Error = summarizeConnectivityError(resp.StatusCode, sample)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (g *Gateway) buildConnectivityRequest(r *http.Request, route provider.Route, account *provider.Account, endpointKey, fallbackPath string, body []byte) (*http.Request, error) {
	upstreamURL := joinUpstreamURL(route.Provider.BaseURL, route.Provider.EndpointPath(endpointKey, fallbackPath), "")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyConfiguredUpstreamHeaders(req.Header, route, account)
	req.ContentLength = int64(len(body))
	return req, nil
}

func connectivityProbeBody(route provider.Route) ([]byte, string, string, error) {
	switch route.Protocol {
	case "openai":
		body, err := json.Marshal(map[string]any{
			"model": route.UpstreamModel,
			"messages": []map[string]string{{
				"role":    "user",
				"content": "ping",
			}},
			"max_tokens": 1,
			"stream":     false,
		})
		return body, "openai_chat_completions", "/v1/chat/completions", err
	case "anthropic":
		body, err := json.Marshal(map[string]any{
			"model": route.UpstreamModel,
			"messages": []map[string]string{{
				"role":    "user",
				"content": "ping",
			}},
			"max_tokens": 1,
		})
		return body, "anthropic_messages", "/v1/messages", err
	default:
		return nil, "", "", fmt.Errorf("unsupported provider protocol %q", route.Protocol)
	}
}

func writeConnectivityTargetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, "provider not found")
	case errors.Is(err, provider.ErrRouteNotFound):
		writeError(w, http.StatusNotFound, "provider route not found")
	case errors.Is(err, provider.ErrPoolNotFound):
		writeError(w, http.StatusNotFound, "pool not found")
	case errors.Is(err, provider.ErrAccountNotFound):
		writeError(w, http.StatusNotFound, "account not found")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func summarizeConnectivityError(statusCode int, sample []byte) string {
	text := strings.TrimSpace(string(sample))
	if len(text) > 512 {
		text = text[:512] + "..."
	}
	if text == "" {
		return http.StatusText(statusCode)
	}

	var payload map[string]any
	if err := json.Unmarshal(sample, &payload); err == nil {
		if errValue, ok := payload["error"]; ok {
			switch typed := errValue.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case map[string]any:
				if message, _ := typed["message"].(string); strings.TrimSpace(message) != "" {
					return strings.TrimSpace(message)
				}
			}
		}
		if message, _ := payload["message"].(string); strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	return text
}
