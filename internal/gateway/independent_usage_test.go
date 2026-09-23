package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func TestIndependentWindowsOnlyStartForAdmittedRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	limits := []store.RateLimit{{Window: "5h", RequestQuota: 1}, {Window: "7d", RequestQuota: 10}}
	app, db, plain := newTestGatewayWithKey(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{Name: "independent", AllowedModels: []string{"public-model"}, RateLimits: limits})
	key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	assertIdle := func() {
		t.Helper()
		statuses, err := app.usage.RateLimitStatuses(key.ID, limits, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range statuses {
			if s.State != "idle" {
				t.Fatalf("rejection/read started window: %+v", s)
			}
		}
	}
	for _, path := range []string{"/api/me/balance", "/api/me/rate-limits"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+plain)
		app.Handler().ServeHTTP(httptest.NewRecorder(), req)
		assertIdle()
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing-model","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("route rejection: %d", rec.Code)
	}
	assertIdle()
	app.env.MaxConcurrentPerKey = 1
	if !app.beginKeyRequest(key.ID) {
		t.Fatal("reserve concurrency")
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	app.endKeyRequest(key.ID)
	if rec.Code != 429 {
		t.Fatalf("concurrency rejection: %d", rec.Code)
	}
	assertIdle()
	before := time.Now().UTC()
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	if rec.Code != 502 {
		t.Fatalf("upstream failure: %d %s", rec.Code, rec.Body)
	}
	statuses, err := app.usage.RateLimitStatuses(key.ID, limits, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.State != "active" || s.StartsAt.Before(before) || s.Requests != 1 {
			t.Fatalf("admission: %+v", s)
		}
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("quota rejection: %d %s", rec.Code, rec.Body)
	}
}
