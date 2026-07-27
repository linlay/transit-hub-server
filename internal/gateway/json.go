package gateway

import (
	"encoding/json"
	"net/http"
)

type errorResponse struct {
	Error string `json:"error"`
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
