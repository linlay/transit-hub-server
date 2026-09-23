package gateway

import (
	"encoding/json"
	"errors"
	"github.com/linlay/transit-hub/internal/store"
	"net/http"
	"strconv"
)

func parseTrafficFilters(r *http.Request) (store.TrafficFilters, int, error) {
	q := r.URL.Query()
	f := store.TrafficFilters{Provider: q.Get("provider"), Status: q.Get("status"), ExclusiveEnd: q.Get("exclusive_end") == "true"}
	for _, field := range []struct {
		name string
		dest *[]string
	}{{"api_key_ids", &f.APIKeyIDs}, {"models", &f.Models}} {
		if raw := q.Get(field.name); raw != "" {
			if err := json.Unmarshal([]byte(raw), field.dest); err != nil {
				return f, 0, errors.New("invalid " + field.name)
			}
			if len(*field.dest) > 100 {
				return f, 0, errors.New("too many filter values")
			}
		}
	}
	if f.Status != "" && f.Status != "success" && f.Status != "failed" {
		return f, 0, errors.New("invalid status")
	}
	offset := 0
	if raw := q.Get("timezone_offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < -720 || offset > 840 {
			return f, 0, errors.New("invalid timezone offset")
		}
	}
	return f, offset, nil
}

func (g *Gateway) trafficAnalytics(w http.ResponseWriter, r *http.Request) {
	query, err := trafficQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if g.telemetryUnavailable() {
		writeTelemetryUnavailable(w)
		return
	}
	result, err := g.store.TrafficAnalytics(r.Context(), query)
	if err != nil {
		writeTelemetryUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
