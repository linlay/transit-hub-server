package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

const deepSeekStream = "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"想\"}}],\"usage\":null}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":null}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":200,\"total_tokens\":1200,\"prompt_cache_hit_tokens\":800,\"prompt_cache_miss_tokens\":200}}\n\n" +
	"data: [DONE]\n\n"

func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func priceTestModel(t *testing.T, db *store.Store) {
	t.Helper()
	hit := int64(40_000)
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{
		Protocol: "openai", PublicModel: "public-model", Unit: "CREDITS",
		InputMicrocreditsPer1MTokens: 2_000_000, OutputMicrocreditsPer1MTokens: 8_000_000, InputCacheHitMicrocreditsPer1MTokens: &hit,
	}); err != nil {
		t.Fatal(err)
	}
}

func onlyLog(t *testing.T, app *Gateway, db *store.Store) store.RequestLogEntry {
	t.Helper()
	if err := app.telemetry.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Items) != 1 {
		t.Fatalf("logs=%+v", logs.Items)
	}
	return logs.Items[0]
}

func streamRequest(plain, acceptEncoding string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	return req
}

// Clients that negotiate gzip receive the upstream bytes unchanged while the
// gateway still bills from the decoded usage, including cache hits.
func TestGzipStreamPassthroughStillReportsUsage(t *testing.T) {
	encoded := gzipBytes(t, deepSeekStream)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "gzip;q=1.0, deflate" {
			t.Errorf("upstream Accept-Encoding=%q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(encoded)
	}))
	defer upstream.Close()
	app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	priceTestModel(t, db)

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, streamRequest(plain, "br, gzip;q=1.0, zstd, deflate"))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(rec.Body.Bytes(), encoded) {
		t.Fatalf("response not passed through: code=%d encoding=%q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	entry := onlyLog(t, app, db)
	if entry.UsageSource != usageSourceReported || entry.Estimated || entry.RequestTokens != 1000 || entry.ResponseTokens != 200 || entry.CacheHitTokens != 800 {
		t.Fatalf("log=%+v", entry)
	}
	// 200 miss * 2 + 800 hit * 0.04 + 200 out * 8 credits per 1M tokens.
	if entry.ChargedMicrocredits != 400+32+1600 || entry.BillingStatus != "charged" {
		t.Fatalf("charge=%d status=%s", entry.ChargedMicrocredits, entry.BillingStatus)
	}
}

// Codings the gateway cannot decode are dropped from the upstream offer, so the
// transport negotiates gzip itself and the client receives identity.
func TestUndecodableOnlyAcceptEncodingFallsBackToTransportGzip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("upstream Accept-Encoding=%q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipBytes(t, deepSeekStream))
	}))
	defer upstream.Close()
	app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, streamRequest(plain, "br"))
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != deepSeekStream {
		t.Fatalf("client body encoding=%q body=%q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	if entry := onlyLog(t, app, db); entry.UsageSource != usageSourceReported || entry.ResponseTokens != 200 {
		t.Fatalf("log=%+v", entry)
	}
}

func TestUsageAnomaliesAreEstimatedAndAlerted(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, body, source string
		complete                     bool
	}{
		{"missing", "", "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\ndata: [DONE]\n\n", usageSourceMissing, true},
		{"undecodable", "br", "\x0b\x02\x80binary", usageSourceUndecodable, true},
		{"in_band_error", "", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n", usageSourceInterrupted, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
			priceTestModel(t, db)
			var alerts bytes.Buffer
			app.usageAlerts = newUsageAlerts(log.New(&alerts, "", 0))

			for range 2 {
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, streamRequest(plain, "gzip"))
				if rec.Body.String() != tc.body {
					t.Fatal("body changed")
				}
			}
			if err := app.telemetry.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			entry := logs.Items[0]
			if entry.UsageSource != tc.source || !entry.Estimated || entry.BillingStatus != "estimated" || entry.RequestTokens == 0 {
				t.Fatalf("log=%+v", entry)
			}
			if tc.name == "missing" && entry.ResponseTokens != 2 {
				t.Fatalf("visible output estimate=%d", entry.ResponseTokens)
			}
			alerted := strings.Count(alerts.String(), "ALERT usage ")
			if want := map[bool]int{true: 1, false: 0}[tc.source != usageSourceInterrupted]; alerted != want {
				t.Fatalf("alerts=%q", alerts.String())
			}

			req := httptest.NewRequest(http.MethodGet, "/admin/providers/usage-anomalies", nil)
			req.Header.Set("Authorization", "Bearer admin")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			var response struct {
				Items []store.UsageAnomaly `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || len(response.Items) != 1 || response.Items[0].UsageSource != tc.source || response.Items[0].Requests != 2 || response.Items[0].Alerting != (tc.source != usageSourceInterrupted) {
				t.Fatalf("anomalies code=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUpstreamErrorRecordsNoTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad input"}}`))
	}))
	defer upstream.Close()
	app, db, plain := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	entry := onlyLog(t, app, db)
	if entry.UsageSource != usageSourceNone || entry.RequestTokens != 0 || entry.Estimated || entry.ErrorType != "upstream_status" {
		t.Fatalf("log=%+v", entry)
	}
	key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedRequests != 1 || key.UsedTokens != 0 {
		t.Fatalf("usage requests=%d tokens=%d", key.UsedRequests, key.UsedTokens)
	}
}

func TestConfiguredUsageMappingOverridesDefaults(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"in":100,"out":7,"cached":60,"prompt_cache_hit_tokens":1}}`))
	}))
	defer upstream.Close()
	cfg := openAIProvider(upstream.URL)
	cfg.Usage = config.UsageConfig{"openai_chat_completions": {Input: config.UsagePaths{"in"}, Output: config.UsagePaths{"out"}, CacheHit: config.UsagePaths{"prompt_cache_hit_tokens"}}}
	cfg.Models[0].Usage = config.UsageConfig{"openai_chat_completions": {CacheHit: config.UsagePaths{"cached"}}}
	app, db, plain := newTestGateway(t, []config.ProviderConfig{cfg})
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, proxyRequest(plain))
	if entry := onlyLog(t, app, db); entry.UsageSource != usageSourceReported || entry.RequestTokens != 100 || entry.ResponseTokens != 7 || entry.CacheHitTokens != 60 {
		t.Fatalf("log=%+v", entry)
	}
}

func TestPerImageBillingWithoutUsageIsNotAnAnomaly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.com/a.png"}]}`))
	}))
	defer upstream.Close()
	cfg := openAIProvider(upstream.URL)
	cfg.Models[0].Type = config.ModelTypeImageGeneration
	app, db, plain := newTestGateway(t, []config.ProviderConfig{cfg})
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{
		Protocol: "openai", PublicModel: "public-model", Unit: "CREDITS",
		Billing: &store.PriceBilling{Mode: "image", ImagePrices: []store.ImagePrice{{ChargedMicrocredits: 2000}}},
	}); err != nil {
		t.Fatal(err)
	}
	var alerts bytes.Buffer
	app.usageAlerts = newUsageAlerts(log.New(&alerts, "", 0))
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"public-model","prompt":"cat"}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	entry := onlyLog(t, app, db)
	if rec.Code != http.StatusOK || entry.UsageSource != usageSourceNone || entry.ImageCount != 1 || entry.ChargedMicrocredits != 2000 || alerts.Len() != 0 {
		t.Fatalf("code=%d log=%+v alerts=%q", rec.Code, entry, alerts.String())
	}
}
