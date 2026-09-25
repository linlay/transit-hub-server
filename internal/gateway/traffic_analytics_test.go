package gateway

import (
	"encoding/json"
	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTrafficFilterParsing(t *testing.T) {
	values := url.Values{"api_key_ids": {`["one","two"]`}, "models": {`["model,a"]`}, "provider": {"provider-a"}, "status": {"failed"}, "timezone_offset": {"480"}, "exclusive_end": {"true"}, "bucket": {"day"}, "from": {"2026-09-01T00:00:00Z"}, "to": {"2026-09-02T00:00:00Z"}}
	q, err := trafficQueryFromRequest(httptest.NewRequest("GET", "/admin/traffic/analytics?"+values.Encode(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Filters.APIKeyIDs) != 2 || q.Filters.Models[0] != "model,a" || q.TimezoneOffset != 480 || !q.Filters.ExclusiveEnd {
		t.Fatalf("wrong query: %+v", q)
	}
	for _, raw := range []string{"api_key_ids=broken", "models=123", "status=other", "timezone_offset=900", "bucket=week", "from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z"} {
		if _, err := trafficQueryFromRequest(httptest.NewRequest("GET", "/admin/traffic/analytics?"+raw, nil)); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestTrafficAnalyticsHTTPAndMatchingLogs(t *testing.T) {
	app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider("http://unused.invalid")})
	key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"model-a", "model-b"} {
		if !app.telemetry.Enqueue(store.RequestLog{APIKeyID: key.ID, APIKeyName: key.Name, PublicModel: model, Provider: "provider-a", StatusCode: 200, RequestTokens: 2, ResponseTokens: 3, ChargedMicrocredits: 10000, CreatedAt: time.Now().UTC()}) {
			t.Fatal("enqueue failed")
		}
	}
	values := url.Values{"models": {`["model-a"]`}, "api_key_ids": {`["` + key.ID + `"]`}, "status": {"success"}, "timezone_offset": {"480"}}
	for _, path := range []string{"/admin/traffic/analytics", "/admin/logs"} {
		req := httptest.NewRequest(http.MethodGet, path+"?"+values.Encode(), nil)
		req.Header.Set("Authorization", "Bearer admin")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if path == "/admin/traffic/analytics" {
			var result store.TrafficAnalytics
			if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Summary.Requests != 1 || len(result.Models) != 1 || result.Models[0].ID != "model-a" {
				t.Fatalf("bad analytics: %+v", result)
			}
		} else {
			var result store.RequestLogListResult
			if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Total != 1 || result.Items[0].PublicModel != "model-a" {
				t.Fatalf("bad logs: %+v", result)
			}
		}
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/traffic/analytics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("analytics must require auth: %d", rec.Code)
	}
}
