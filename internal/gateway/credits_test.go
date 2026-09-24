package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func TestCreditsConcurrentOverageAndBalanceWithoutTelemetry(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	}))
	defer upstream.Close()
	app, db, plain := newTestGatewayWithKey(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{Name: "credits", CostQuotaMicro: 10, RateLimits: []store.RateLimit{{Window: "1h", CostQuotaMicro: 10}}})
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: "openai", PublicModel: "public-model", Currency: "CNY", InputCostMicroPer1MTokens: 1_000_000, OutputCostMicroPer1MTokens: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	app.env.MaxConcurrentPerKey = 2
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, proxyRequest(plain))
			if rec.Code != 200 {
				t.Errorf("request: %d %s", rec.Code, rec.Body)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("concurrent requests failed to enter")
		}
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	if rec.Code != 429 {
		t.Errorf("concurrency: %d", rec.Code)
	}
	var concurrencyError concurrencyLimitErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &concurrencyError); err != nil {
		t.Error(err)
	}
	if concurrencyError.Code != "api_key_concurrency_limit_exceeded" || !concurrencyError.Retryable || concurrencyError.Scope != "api_key" || strings.Contains(concurrencyError.Error, "exhausted") {
		t.Errorf("unexpected concurrency error: %+v", concurrencyError)
	}
	close(release)
	wg.Wait()
	key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedCostMicro != 14 {
		t.Fatalf("used=%d", key.UsedCostMicro)
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	if rec.Code != 429 {
		t.Fatalf("quota did not block: %d", rec.Code)
	}
	// Metadata remains accessible to exhausted keys and does not require telemetry.
	app.telemetry = nil
	req := httptest.NewRequest("GET", "/api/me/balance", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var balance selfBalanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &balance); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || balance.UsedCostMicro != 14 || balance.CostRemainingMicro != -4 || balance.Unlimited || balance.BillingVersion != "credits_v1" || len(balance.Items) != 1 || balance.Items[0].CostRemainingMicro != -4 {
		t.Fatalf("balance=%+v body=%s", balance, rec.Body)
	}
}

func TestCreditsFailureFreeSnapshotAndStartWindow(t *testing.T) {
	app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://example.invalid")})
	key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	price := store.ModelPrice{Currency: "CNY", InputCostMicroPer1MTokens: 1_000_000, OutputCostMicroPer1MTokens: 1_000_000, Billing: store.PriceBilling{Mode: "tokens"}}
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Minute)
	req := withBillingContext(proxyRequest(plain), start)
	app.logCompletedRequest(req, key, store.RequestLog{StatusCode: 502, ModelPrice: &price, RequestTokens: 100, Estimated: true})
	app.logCompletedRequest(req, key, store.RequestLog{StatusCode: 200, ModelPrice: &price, RequestTokens: 3, ResponseTokens: 4})
	price.Billing.Mode = "free"
	app.logCompletedRequest(req, key, store.RequestLog{StatusCode: 200, ModelPrice: &price, RequestTokens: 100})
	if total := app.usage.Total(key.ID); total.UsedCostMicro != 7 {
		t.Fatalf("charged failure/free: %+v", total)
	}
	limits := []store.RateLimit{{Window: "1h", CostQuotaMicro: 5}}
	before, _ := app.usage.RateLimitStatuses(key.ID, limits, start)
	now, _ := app.usage.RateLimitStatuses(key.ID, limits, time.Now().UTC())
	if before[0].CostMicro != 7 || now[0].CostMicro != 0 {
		t.Fatalf("wrong window: %+v %+v", before, now)
	}
	if err := app.telemetry.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, log := range logs.Items {
		seen[log.BillingStatus] = true
		if log.StartedAt == nil || !log.StartedAt.Equal(start) || len(log.PriceSnapshot) == 0 {
			t.Fatalf("missing metadata %+v", log)
		}
	}
	if !seen["not_charged"] || !seen["charged"] || !seen["free"] {
		t.Fatalf("statuses %v", seen)
	}
}

func TestCreditsAdminContractAndPricePatch(t *testing.T) {
	app, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://example.invalid")})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer admin")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	created := call("POST", "/admin/api-keys", `{"name":"budget","allowed_models":["public-model"],"cost_quota_micro":1000000}`)
	var key createAPIKeyResponse
	if json.Unmarshal(created.Body.Bytes(), &key) != nil || created.Code != 201 || key.CostQuotaMicro != 1000000 {
		t.Fatalf("create %s", created.Body)
	}
	changed := call("PATCH", "/admin/api-keys/"+key.ID, `{"name":"renamed"}`)
	var updated apiKeyResponse
	json.Unmarshal(changed.Body.Bytes(), &updated)
	if updated.CostQuotaMicro != 1000000 {
		t.Fatalf("patch reset quota %s", changed.Body)
	}
	if rec := call("PATCH", "/admin/api-keys/"+key.ID, `{"cost_quota_micro":-1}`); rec.Code != 400 {
		t.Fatal("accepted negative quota")
	}
	price := call("POST", "/admin/model-prices", `{"protocol":"openai","public_model":"public-model","input_cost_micro_per_1m_tokens":1000000,"output_cost_micro_per_1m_tokens":2000000,"billing":{"mode":"tokens"}}`)
	var p store.ModelPrice
	json.Unmarshal(price.Body.Bytes(), &p)
	if price.Code != 201 {
		t.Fatalf("price %s", price.Body)
	}
	changed = call("PATCH", "/admin/model-prices/"+p.ID, `{"billing":{"mode":"tokens","max_output_tokens":1024}}`)
	json.Unmarshal(changed.Body.Bytes(), &p)
	if changed.Code != 200 || p.InputCostMicroPer1MTokens != 1000000 || p.OutputCostMicroPer1MTokens != 2000000 {
		t.Fatalf("partial price patch %s", changed.Body)
	}
	if rec := call("POST", "/admin/model-prices", `{"protocol":"openai","public_model":"zero"}`); rec.Code != 400 {
		t.Fatal("implicit free accepted")
	}
}

func TestImageBilling(t *testing.T) {
	price := store.ModelPrice{Billing: store.PriceBilling{Mode: "image", ImagePrices: []store.ImagePrice{{CostMicro: 2000}, {Size: "1024x1024", Quality: "hd", CostMicro: 5000}}}}
	body, err := parseProxyBody("openai_image_generations", "application/json", []byte(`{"model":"image","size":"1024x1024","quality":"hd","n":2}`))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := prepareBillingRequest(&body, &price, "image-generation", "openai_image_generations")
	if err != nil || unit != 5000 {
		t.Fatalf("unit=%d %v", unit, err)
	}
	if cost := imageResponseCost(&price, unit, []byte(`{"data":[{"url":"a"},{"url":"b"}]}`), 200); cost != 10000 {
		t.Fatalf("image cost=%d", cost)
	}

}

func TestSSEUsageAfterSampleLimit(t *testing.T) {
	// Usage in the final event must survive a response longer than the log sample.
	body := strings.Repeat("data: {\"choices\":[]}\n\n", responseSampleLimit/20+1) + "data: {\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":456}}\n\n"
	result, err := copyResponse(httptest.NewRecorder(), strings.NewReader(body), true)
	if err != nil || len(result.Sample) != responseSampleLimit || result.Usage.Request != 123 || result.Usage.Response != 456 {
		t.Fatalf("late SSE usage sample=%d usage=%+v err=%v", len(result.Sample), result.Usage, err)
	}
}

func TestDefaultConcurrencyLimitPerKey(t *testing.T) {
	app := &Gateway{}
	for i := 0; i < 16; i++ {
		if !app.beginKeyRequest("a") {
			t.Fatalf("request %d rejected", i+1)
		}
	}
	if app.beginKeyRequest("a") {
		t.Fatal("17th request accepted")
	}
	if !app.beginKeyRequest("b") {
		t.Fatal("independent key rejected")
	}
	app.endKeyRequest("a")
	if !app.beginKeyRequest("a") {
		t.Fatal("released slot unavailable")
	}
}

func TestBillingPreservesClientOutputBudget(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, billing := range []string{`{"mode":"tokens"}`, `{"mode":"free"}`, `{"mode":"tokens","default_max_output_tokens":4096,"max_output_tokens":8192}`} {
			for _, budget := range []string{"", `,"max_tokens":65536`, `,"max_completion_tokens":65536`} {
				for _, stream := range []bool{false, true} {
					request := `{"model":"chat"` + budget
					if stream {
						request += `,"stream":true`
					}
					request += `}`
					t.Run(protocol+"/"+billing+"/"+request, func(t *testing.T) {
						var price store.ModelPrice
						if err := json.Unmarshal([]byte(billing), &price.Billing); err != nil {
							t.Fatal(err)
						}
						endpoint := "openai_chat_completions"
						if protocol == "anthropic" {
							endpoint = "anthropic_messages"
						}
						body, err := parseProxyBody(endpoint, "application/json", []byte(request))
						if err != nil {
							t.Fatal(err)
						}
						if _, err := prepareBillingRequest(&body, &price, "chat", endpoint); err != nil {
							t.Fatal(err)
						}
						var before, after map[string]json.RawMessage
						if err := json.Unmarshal([]byte(request), &before); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(body.Body, &after); err != nil {
							t.Fatal(err)
						}
						for _, name := range []string{"max_tokens", "max_completion_tokens"} {
							if string(before[name]) != string(after[name]) {
								t.Fatalf("%s changed: %s", name, body.Body)
							}
						}
						if protocol == "openai" && stream {
							var opts struct {
								IncludeUsage bool `json:"include_usage"`
							}
							if err := json.Unmarshal(after["stream_options"], &opts); err != nil || !opts.IncludeUsage {
								t.Fatalf("missing stream usage: %s", body.Body)
							}
						}
					})
				}
			}
		}
	}
}

func TestImageTokenBillingAndMissingUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage, status string
		cost                int64
	}{
		{"usage", `,"usage":{"input_tokens":100,"output_tokens":200,"input_tokens_details":{"cached_tokens":20}}`, "charged", 44975},
		{"missing", "", "unavailable", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"data":[{"b64_json":"AAAA"}]` + tc.usage + `}`))
			}))
			defer upstream.Close()
			provider := openAIProvider(upstream.URL)
			provider.Models[0].Type = "image-generation"
			app, db, plain := newTestGateway(t, []config.ProviderConfig{provider})
			hit := int64(8750000)
			_, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: "openai", PublicModel: "public-model", Currency: "CNY", InputCostMicroPer1MTokens: 35000000, InputCacheHitCostMicroPer1MTokens: &hit, OutputCostMicroPer1MTokens: 210000000, Billing: &store.PriceBilling{Mode: "tokens"}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"public-model","prompt":"pet","size":"1024x1024","n":1}`))
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("response: %d %s", rec.Code, rec.Body)
			}
			key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
			if err != nil {
				t.Fatal(err)
			}
			if key.UsedCostMicro != tc.cost {
				t.Fatalf("cost: %d", key.UsedCostMicro)
			}
			if err := app.telemetry.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Items) != 1 || logs.Items[0].BillingStatus != tc.status {
				t.Fatalf("billing logs: %+v", logs.Items)
			}
		})
	}
}
