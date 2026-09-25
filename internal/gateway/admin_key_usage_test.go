package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func TestListAPIKeysIncludesWindowUsage(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	key, err := db.CreateAPIKey(t.Context(), store.CreateAPIKeyParams{Name: "windowed", RateLimits: []store.RateLimit{
		{Window: "5h", RequestQuota: 10, TokenQuota: 100, QuotaMicrocredits: 1000},
		{Window: "7d", RequestQuota: 50, TokenQuota: 1000, QuotaMicrocredits: 5000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Old consumption belongs only to the lifetime counter.
	app.usage.Record(key.ID, 100, 200, 9000, now.Add(-8*24*time.Hour))
	bindings, err := app.usage.Admit(t.Context(), key.APIKey, now)
	if err != nil {
		t.Fatal(err)
	}
	app.usage.Record(key.ID, 3, 4, 250, now, bindings)
	req := httptest.NewRequest(http.MethodGet, "/admin/api-keys", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Items []apiKeyResponse `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 {
		t.Fatalf("total = %d", result.Total)
	}
	for _, item := range result.Items {
		if item.ID != key.ID {
			if len(item.RateLimitUsage) != 0 {
				t.Fatal("unconfigured key has windows")
			}
			continue
		}
		if item.UsedRequests != 2 || item.UsedTokens != 307 || item.UsedMicrocredits != 9250 {
			t.Fatalf("lifetime: %#v", item)
		}
		if len(item.RateLimitUsage) != 2 || item.RateLimitUsageUnavailable {
			t.Fatalf("missing windows: %#v", item)
		}
		for i, status := range item.RateLimitUsage {
			if status.Window != []string{"5h", "7d"}[i] || status.Requests != 1 || status.Tokens != 7 || status.ChargedMicrocredits != 250 {
				t.Fatalf("window: %#v", status)
			}
			if !status.ResetsAt.After(now) || status.StartsAt.After(now) {
				t.Fatalf("bounds: %#v", status)
			}
			if status.RequestRemaining != status.RequestQuota-1 || status.TokenRemaining != status.TokenQuota-7 || status.RemainingMicrocredits != status.QuotaMicrocredits-250 {
				t.Fatalf("remaining: %#v", status)
			}
		}
	}
}

func TestListAPIKeysUsageUnavailableKeepsManagementAvailable(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	_, err := db.CreateAPIKey(t.Context(), store.CreateAPIKeyParams{Name: "unavailable", RateLimits: []store.RateLimit{{Window: "5h", RequestQuota: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	db.AttachRuntime(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api-keys?search=unavailable", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Items []apiKeyResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !result.Items[0].RateLimitUsageUnavailable || len(result.Items[0].RateLimitUsage) != 0 {
		t.Fatalf("response: %#v", result)
	}
}
