package gateway

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/linlay/transit-hub/internal/store"
)

const usageAlertInterval = 5 * time.Minute

// usageAnomaly is a successful response whose usage the gateway could not read.
type usageAnomaly struct {
	Source            string
	Provider          string
	PublicModel       string
	Endpoint          string
	ContentEncoding   string
	UpstreamRequestID string
	DecodeErr         string
}

type usageAlertKey struct {
	source, provider, model, endpoint string
}

type usageAlertState struct {
	lastLogged time.Time
	suppressed int64
}

// usageAlerts logs usage anomalies, at most once per interval for each
// provider, model, endpoint and source. Persistent counts live in request logs.
type usageAlerts struct {
	mu     sync.Mutex
	logger *log.Logger
	now    func() time.Time
	states map[usageAlertKey]*usageAlertState
}

func newUsageAlerts(logger *log.Logger) *usageAlerts {
	return &usageAlerts{logger: logger, now: time.Now, states: map[usageAlertKey]*usageAlertState{}}
}

func (a *usageAlerts) report(anomaly usageAnomaly) {
	if a == nil {
		return
	}
	key := usageAlertKey{anomaly.Source, anomaly.Provider, anomaly.PublicModel, anomaly.Endpoint}
	now := a.now()
	a.mu.Lock()
	state := a.states[key]
	if state == nil {
		state = &usageAlertState{}
		a.states[key] = state
	}
	if !state.lastLogged.IsZero() && now.Sub(state.lastLogged) < usageAlertInterval {
		state.suppressed++
		a.mu.Unlock()
		return
	}
	suppressed := state.suppressed
	state.lastLogged = now
	state.suppressed = 0
	a.mu.Unlock()
	a.logger.Printf("ALERT usage %s: provider=%s model=%s endpoint=%s content_encoding=%q upstream_request_id=%q decode_error=%q suppressed_since_last=%d",
		anomaly.Source, anomaly.Provider, anomaly.PublicModel, anomaly.Endpoint, anomaly.ContentEncoding, anomaly.UpstreamRequestID, anomaly.DecodeErr, suppressed)
}

// usageSources summarizes requests by provider, model and usage source;
// alerting rows are successful requests whose usage could not be read.
func (g *Gateway) usageSources(w http.ResponseWriter, r *http.Request) {
	g.usageReport(w, r, func(query store.ProviderUsageQuery) (any, error) {
		return g.store.UsageSources(r.Context(), query)
	})
}

// usageCalibrations compares reported usage with the estimators per model.
func (g *Gateway) usageCalibrations(w http.ResponseWriter, r *http.Request) {
	g.usageReport(w, r, func(query store.ProviderUsageQuery) (any, error) {
		return g.store.UsageCalibrations(r.Context(), query)
	})
}

func (g *Gateway) usageReport(w http.ResponseWriter, r *http.Request, load func(store.ProviderUsageQuery) (any, error)) {
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if g.telemetryUnavailable() {
		writeTelemetryUnavailable(w)
		return
	}
	items, err := load(store.ProviderUsageQuery{From: from, To: to})
	if err != nil {
		writeTelemetryUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
