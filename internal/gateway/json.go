package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type errorResponse struct {
	Error string `json:"error"`
}

type concurrencyLimitErrorResponse struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Scope     string `json:"scope"`
	Retryable bool   `json:"retryable"`
}

type componentErrorResponse struct {
	Error     string `json:"error"`
	Component string `json:"component"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeComponentError(w http.ResponseWriter, status int, message, component string) {
	writeJSON(w, status, componentErrorResponse{Error: message, Component: component})
}

func writeTelemetryUnavailable(w http.ResponseWriter) {
	writeComponentError(w, http.StatusServiceUnavailable, "telemetry unavailable", "telemetry")
}

func (g *Gateway) telemetryUnavailable() bool {
	return g.telemetry == nil || g.telemetry.Degraded()
}

func (g *Gateway) degradedComponents() []string {
	components := []string{}
	if g.usage == nil || g.usage.Degraded() {
		components = append(components, "usage")
	}
	if g.telemetry == nil || g.telemetry.Degraded() {
		components = append(components, "telemetry")
	}
	return components
}

func withDegradedComponent(components []string, component string) []string {
	for _, existing := range components {
		if existing == component {
			return components
		}
	}
	return append(components, component)
}

// Reject the retired money fields explicitly; unrelated deprecated fields
// (such as output token defaults) retain their existing ignored behavior.
func decodeBillingJSON(r *http.Request, target any) error {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return err
	}
	var fields any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var check func(any) error
	check = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if key == "currency" || strings.Contains(key, "cost_micro") || key == "cost_quota_micro" || key == "cost_remaining_micro" {
					return fmt.Errorf("retired billing field %s; use native Credits fields", key)
				}
				if err := check(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range v {
				if err := check(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(fields); err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
