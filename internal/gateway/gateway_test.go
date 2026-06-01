package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
)

func TestOpenAIProxyRewritesModelAuthAndRecordsUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream auth: %q", got)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Fatalf("gateway x-api-key leaked upstream: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-model" {
			t.Fatalf("model was not rewritten: %#v", body["model"])
		}
		if body["temperature"].(float64) != 0.2 {
			t.Fatalf("non-model field was not preserved: %#v", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"public-model",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0.2
	}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedRequests != 1 {
		t.Fatalf("used_requests = %d", key.UsedRequests)
	}
	if key.UsedTokens != 7 {
		t.Fatalf("used_tokens = %d", key.UsedTokens)
	}
}

func TestAnthropicProxyUsesNativePathAndXAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "upstream-key" {
			t.Fatalf("unexpected upstream x-api-key: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization leaked upstream: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "claude-upstream" {
			t.Fatalf("model was not rewritten: %#v", body["model"])
		}
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"input_tokens":2,"output_tokens":5}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{anthropicProvider(upstream.URL)})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{
		"model":"claude-public",
		"messages":[{"role":"user","content":"hi"}],
		"max_tokens":64
	}`))
	req.Header.Set("x-api-key", plainKey)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedTokens != 7 {
		t.Fatalf("used_tokens = %d", key.UsedTokens)
	}
}

func TestQuotaAndForcedExpirationRejectRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGatewayWithKey(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{
		Name:         "limited",
		RequestQuota: 1,
		TokenQuota:   0,
	})

	first := proxyRequest(plainKey)
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := proxyRequest(plainKey)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(second.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	forced := true
	if _, err := db.UpdateAPIKey(second.Context(), key.ID, store.APIKeyPatch{ForcedExpired: &forced}); err != nil {
		t.Fatal(err)
	}

	third := proxyRequest(plainKey)
	thirdRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(thirdRec, third)
	if thirdRec.Code != http.StatusUnauthorized {
		t.Fatalf("third status = %d, body = %s", thirdRec.Code, thirdRec.Body.String())
	}
}

func TestCircuitOpensAfterUpstreamFailure(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})

	first := proxyRequest(plainKey)
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d", firstRec.Code)
	}

	second := proxyRequest(plainKey)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d", got)
	}
}

func TestAdminCreateKeyReturnsPlainTextOnce(t *testing.T) {
	app, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})

	createReq := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{
		"name":"admin-created",
		"request_quota":10,
		"token_quota":100
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if key, _ := created["key"].(string); !strings.HasPrefix(key, "th_") {
		t.Fatalf("plain key missing from create response: %#v", created["key"])
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/api-keys", nil)
	listReq.Header.Set("x-admin-token", "admin")
	listRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if bytes.Contains(listRec.Body.Bytes(), []byte(`"key"`)) {
		t.Fatalf("list response leaked plain key: %s", listRec.Body.String())
	}
}

func TestAdminLoginCookieAuthorizesRequests(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	if _, err := db.CreateAdminUser(t.Context(), store.CreateAdminUserParams{
		Username: "operator",
		Password: "secret-pass",
	}); err != nil {
		t.Fatal(err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/login", bytes.NewBufferString(`{
		"username":"operator",
		"password":"secret-pass"
	}`))
	loginReq.Header.Set("Origin", "http://localhost:5173")
	loginRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	if got := loginRec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("cors credentials header = %q", got)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != adminSessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("session cookie not set correctly: %#v", cookies)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/admin/auth/me", nil)
	meReq.AddCookie(cookies[0])
	meRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", meRec.Code, meRec.Body.String())
	}
	if !bytes.Contains(meRec.Body.Bytes(), []byte(`"username":"operator"`)) {
		t.Fatalf("me response missing user: %s", meRec.Body.String())
	}
}

func TestSoftDeletedAPIKeyCannotProxyButHistoryRemains(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	req := proxyRequest(plainKey)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}
	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api-keys/"+key.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer admin")
	deleteRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	blockedReq := proxyRequest(plainKey)
	blockedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusUnauthorized {
		t.Fatalf("blocked status = %d, body = %s", blockedRec.Code, blockedRec.Body.String())
	}

	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 {
		t.Fatalf("history logs total = %d", logs.Total)
	}
}

func TestProxyRecordsSessionAndEstimatedCost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{
		Protocol:                      "openai",
		PublicModel:                   "public-model",
		InputCostMicroUSDPer1MTokens:  2_000_000,
		OutputCostMicroUSDPer1MTokens: 4_000_000,
		Currency:                      "USD",
	}); err != nil {
		t.Fatal(err)
	}

	req := proxyRequest(plainKey)
	req.Header.Set("x-device-id", "macbook-pro")
	req.Header.Set("x-source", "codex")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 || logs.Items[0].DeviceID != "macbook-pro" || logs.Items[0].Source != "codex" {
		t.Fatalf("unexpected log session fields: %#v", logs)
	}
	if logs.Items[0].CostMicroUSD != 22 {
		t.Fatalf("cost_microusd = %d", logs.Items[0].CostMicroUSD)
	}
	sessions, err := db.ListAPISessions(t.Context(), store.APISessionQuery{
		APIKeyID:     key.ID,
		ActiveWindow: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessions.Total != 1 || !sessions.Items[0].Active || sessions.Items[0].TokenCount != 7 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func newTestGateway(t *testing.T, providers []config.ProviderConfig) (*Gateway, *store.Store, string) {
	t.Helper()
	return newTestGatewayWithKey(t, providers, store.CreateAPIKeyParams{Name: "test"})
}

func newTestGatewayWithKey(t *testing.T, providers []config.ProviderConfig, keyParams store.CreateAPIKeyParams) (*Gateway, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	registry, err := provider.NewRegistry(providers, provider.CircuitOptions{
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateAPIKey(t.Context(), keyParams)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		Env: config.Env{
			AdminToken:              "admin",
			AdminSessionTTL:         24 * time.Hour,
			CORSAllowedOrigins:      []string{"http://localhost:5173"},
			SessionActiveWindow:     5 * time.Minute,
			ConfigDir:               t.TempDir(),
			UpstreamTimeout:         3 * time.Second,
			CircuitFailureThreshold: 1,
			CircuitCooldown:         time.Hour,
		},
		Store:    db,
		Registry: registry,
		Client:   upstreamClient(t),
	})
	return app, db, created.PlainText
}

func upstreamClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 3 * time.Second}
}

func proxyRequest(plainKey string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"public-model",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	return req
}

func openAIProvider(baseURL string) config.ProviderConfig {
	return config.ProviderConfig{
		Name:        "test-openai",
		Protocol:    "openai",
		BaseURL:     baseURL,
		DefaultPool: "primary",
		Models: []config.ModelConfig{{
			Public:   "public-model",
			Upstream: "upstream-model",
			Pool:     "primary",
		}},
		Pools: []config.PoolConfig{{
			Name: "primary",
			Accounts: []config.AccountConfig{{
				Name:   "acct1",
				APIKey: "upstream-key",
				Weight: 1,
			}},
		}},
	}
}

func anthropicProvider(baseURL string) config.ProviderConfig {
	return config.ProviderConfig{
		Name:        "test-anthropic",
		Protocol:    "anthropic",
		BaseURL:     baseURL,
		DefaultPool: "primary",
		Models: []config.ModelConfig{{
			Public:   "claude-public",
			Upstream: "claude-upstream",
			Pool:     "primary",
		}},
		Pools: []config.PoolConfig{{
			Name: "primary",
			Accounts: []config.AccountConfig{{
				Name:   "acct1",
				APIKey: "upstream-key",
				Weight: 1,
			}},
		}},
	}
}
