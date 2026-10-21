package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
	"github.com/linlay/transit-hub/internal/usage"
)

type ttsTransport func(*http.Request) (*http.Response, error)

func (f ttsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ttsProvider(protocol string) config.ProviderConfig {
	cfg := openAIProvider("https://speech.example")
	cfg.Name, cfg.Protocol = protocol, protocol
	cfg.Models[0].Type = config.ModelTypeTTS
	return cfg
}

func TestMiniMaxTTSReusesExistingChatProvider(t *testing.T) {
	cfg := openAIProvider("https://minimax.example")
	cfg.Name = "minimax"
	cfg.Models = append(cfg.Models, config.ModelConfig{Public: "minimax-speech-2.8-hd", Upstream: "speech-2.8-hd", Protocol: "minimax_tts", Type: "tts"})
	app, db, key := newTestGateway(t, []config.ProviderConfig{cfg})
	price := int64(10000)
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: "minimax_tts", PublicModel: "minimax-speech-2.8-hd", Billing: &store.PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app.client = &http.Client{Transport: ttsTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Error("existing MiniMax account was not reused")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response := `{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`
		switch r.URL.Path {
		case "/v1/chat/completions":
			if body["model"] != "upstream-model" {
				t.Error("chat model changed")
			}
		case "/v1/t2a_v2":
			if body["model"] != "speech-2.8-hd" {
				t.Error("TTS model not rewritten")
			}
			response = `{"data":{"audio":"ff00"},"extra_info":{"usage_characters":12},"base_resp":{"status_code":0}}`
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/v1/chat/completions", `{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`, 200},
		{"/minimax/v1/t2a_v2", `{"model":"minimax-speech-2.8-hd","text":"hello","stream":false}`, 200},
		{"/v1/chat/completions", `{"model":"minimax-speech-2.8-hd"}`, 404},
		{"/minimax/v1/t2a_v2", `{"model":"public-model"}`, 404},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("cross-protocol request reached upstream: calls=%d", calls)
	}
	got, err := db.FindAPIKeyByPlainText(t.Context(), key)
	if err != nil || got.UsedRequests != 2 || got.UsedTokens != 7 || got.UsedMicrocredits != 12 {
		t.Fatalf("chat/TTS accounting changed: %+v %v", got, err)
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "minimax-speech-2.8-hd") || !strings.Contains(rec.Body.String(), "public-model") {
		t.Fatalf("chat model catalog leaked TTS: %s", rec.Body.String())
	}
}

func TestTTSNativePassthroughAndCharacterBilling(t *testing.T) {
	for _, protocol := range []string{usage.EndpointMiniMaxTTS, usage.EndpointDashScopeTTS} {
		for _, compressed := range []bool{false, true} {
			t.Run(protocol+map[bool]string{true: "/gzip", false: "/plain"}[compressed], func(t *testing.T) {
				app, db, key := newTestGatewayWithKey(t, []config.ProviderConfig{ttsProvider(protocol)}, store.CreateAPIKeyParams{Name: "tts", QuotaMicrocredits: 1_000_000})
				price := int64(350_000)
				_, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: protocol, PublicModel: "public-model", Billing: &store.PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}})
				if err != nil {
					t.Fatal(err)
				}
				wire := `{"data":{"audio":"ff00"},"extra_info":{"usage_characters":12},"trace_id":"native-id","base_resp":{"status_code":0}}`
				path, upstream := "/minimax/v1/t2a_v2", "/v1/t2a_v2"
				if protocol == usage.EndpointDashScopeTTS {
					wire = `{"output":{"audio":{"url":"https://audio.example/signed?token=keep"}},"usage":{"characters":12},"request_id":"native-id"}`
					path, upstream = "/bailian/v1/services/aigc/multimodal-generation/generation", "/api/v1/services/aigc/multimodal-generation/generation"
				}
				encoded := []byte(wire)
				if compressed {
					encoded = gzipBytes(t, wire)
				}
				calls := 0
				app.client = &http.Client{Transport: ttsTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.Path != upstream || r.URL.RawQuery != "feature=on" {
						t.Errorf("url=%s", r.URL)
					}
					if r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("X-Api-Key") != "" {
						t.Error("auth not replaced")
					}
					var payload map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if string(payload["model"]) != `"upstream-model"` || string(payload["future"]) != `{"n":9007199254740993}` || string(payload["voice_setting"]) != `{"voice_id":"v","speed":1.2}` {
						t.Errorf("payload=%s", payload)
					}
					if _, ok := payload["stream_options"]; ok {
						t.Error("chat option injected")
					}
					headers := http.Header{"Content-Type": {"application/json"}}
					if compressed {
						headers.Set("Content-Encoding", "gzip")
					}
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(encoded))}, nil
				})}
				req := httptest.NewRequest("POST", path+"?feature=on", strings.NewReader(`{"model":"public-model","stream":false,"text":"hello","input":{"text":"hello","voice":"Cherry"},"voice_setting":{"voice_id":"v","speed":1.2},"future":{"n":9007199254740993}}`))
				req.Header.Set("X-Api-Key", key)
				req.Header.Set("Accept-Encoding", "gzip")
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), encoded) || calls != 1 {
					t.Fatalf("code=%d calls=%d", rec.Code, calls)
				}
				entry := onlyLog(t, app, db)
				if entry.Characters != 12 || entry.ChargedMicrocredits != 420 || entry.RequestTokens != 0 || entry.ResponseTokens != 0 || entry.Estimated || entry.UsageSource != "reported" || entry.UpstreamRequestID != "native-id" {
					t.Fatalf("entry=%+v", entry)
				}
				found, err := db.FindAPIKeyByPlainText(t.Context(), key)
				if err != nil || found.UsedTokens != 0 || found.UsedMicrocredits != 420 || found.UsedRequests != 1 {
					t.Fatalf("key=%+v err=%v", found, err)
				}
				prices := httptest.NewRecorder()
				pr := httptest.NewRequest("GET", "/api/me/prices", nil)
				pr.Header.Set("Authorization", "Bearer "+key)
				app.Handler().ServeHTTP(prices, pr)
				if prices.Code != 200 || !strings.Contains(prices.Body.String(), `"microcredits_per_10k_characters":"350000"`) {
					t.Fatalf("self prices=%s", prices.Body.String())
				}
				calibrations, err := app.telemetry.UsageCalibrations(t.Context(), store.ProviderUsageQuery{})
				if err != nil || len(calibrations) != 0 {
					t.Fatalf("tts polluted token calibration: %+v %v", calibrations, err)
				}
			})
		}
	}
}

func TestTTSFailureMissingUsageAndZero(t *testing.T) {
	for _, tc := range []struct {
		name, body, source, billing, failure string
		status                               int
	}{
		{"business-error", `{"base_resp":{"status_code":2013},"extra_info":{"usage_characters":99}}`, "none", "not_charged", "upstream_business_error", 200},
		{"missing", `{"data":{"audio":"aa"}}`, "missing", "unavailable", "", 200},
		{"zero", `{"extra_info":{"usage_characters":0},"base_resp":{"status_code":0}}`, "reported", "charged", "", 200},
		{"http-error", `{"extra_info":{"usage_characters":99}}`, "none", "not_charged", "upstream_status", 429},
		{"invalid", `{"extra_info":{"usage_characters":`, "missing", "unavailable", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, db, key := newTestGateway(t, []config.ProviderConfig{ttsProvider(usage.EndpointMiniMaxTTS)})
			price := int64(1_000_000)
			if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: usage.EndpointMiniMaxTTS, PublicModel: "public-model", Billing: &store.PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}}); err != nil {
				t.Fatal(err)
			}
			app.client = &http.Client{Transport: ttsTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			req := httptest.NewRequest("POST", "/minimax/v1/t2a_v2", strings.NewReader(`{"model":"public-model","text":"hello"}`))
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Body.String() != tc.body {
				t.Fatalf("response changed: %d %s", rec.Code, rec.Body.String())
			}
			entry := onlyLog(t, app, db)
			if entry.UsageSource != tc.source || entry.BillingStatus != tc.billing || entry.ErrorType != tc.failure || entry.ChargedMicrocredits != 0 || entry.Characters != 0 || entry.RequestTokens != 0 || entry.ResponseTokens != 0 || entry.Estimated {
				t.Fatalf("entry=%+v", entry)
			}
		})
	}
}

func TestTTSRejectsStreamingAndCrossProviderRoutes(t *testing.T) {
	for _, tc := range []struct {
		path, body, header string
		status             int
	}{
		{"/minimax/v1/t2a_v2", `{"model":"public-model","stream":true}`, "", 400},
		{"/minimax/v1/t2a_v2", `{"model":"public-model"}`, "enable", 400},
		{"/bailian/v1/services/aigc/multimodal-generation/generation", `{"model":"public-model"}`, "", 404},
		{"/v1/t2a_v2", `{"model":"public-model"}`, "", 404},
	} {
		t.Run(tc.path+tc.body+tc.header, func(t *testing.T) {
			app, _, key := newTestGateway(t, []config.ProviderConfig{ttsProvider(usage.EndpointMiniMaxTTS)})
			app.client = &http.Client{Transport: ttsTransport(func(*http.Request) (*http.Response, error) {
				t.Error("rejected request reached upstream")
				return nil, io.EOF
			})}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-DashScope-SSE", tc.header)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTTSAdminMetadataProbeAndLargeResponse(t *testing.T) {
	app, db, key := newTestGateway(t, []config.ProviderConfig{ttsProvider(usage.EndpointMiniMaxTTS)})
	wire := `{"data":{"audio":"` + strings.Repeat("ab", 5*1024*1024) + `"},"extra_info":{"usage_characters":987},"base_resp":{"status_code":0},"trace_id":"large-id"}`
	app.client = &http.Client{Transport: ttsTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/t2a_v2" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})}
	req := httptest.NewRequest("POST", "/minimax/v1/t2a_v2", strings.NewReader(`{"model":"public-model","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != wire {
		t.Fatal("large response was changed")
	}
	if entry := onlyLog(t, app, db); entry.Characters != 987 || entry.UsageSource != "reported" || entry.BillingStatus != "unpriced" {
		t.Fatalf("entry=%+v", entry)
	}

	meta := httptest.NewRequest("GET", "/admin/models", nil)
	meta.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, meta)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"gateway_path":"/minimax/v1/t2a_v2"`) || !strings.Contains(rec.Body.String(), `"upstream_path":"/v1/t2a_v2"`) {
		t.Fatalf("metadata=%s", rec.Body.String())
	}
	probe := httptest.NewRequest("POST", "/admin/providers/test", strings.NewReader(`{"provider":"minimax_tts","public_model":"public-model"}`))
	probe.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, probe)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"upstream_request_id":"large-id"`) {
		t.Fatalf("probe=%d %s", rec.Code, rec.Body.String())
	}
}

func TestTTSRequiresPriceForMonetaryQuota(t *testing.T) {
	app, db, key := newTestGatewayWithKey(t, []config.ProviderConfig{ttsProvider(usage.EndpointMiniMaxTTS)}, store.CreateAPIKeyParams{Name: "limited", QuotaMicrocredits: 1000})
	app.client = &http.Client{Transport: ttsTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unpriced request reached upstream")
		return nil, io.EOF
	})}
	req := httptest.NewRequest("POST", "/minimax/v1/t2a_v2", strings.NewReader(`{"model":"public-model","text":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if entry := onlyLog(t, app, db); entry.ErrorType != "price_unavailable" || entry.ChargedMicrocredits != 0 {
		t.Fatalf("entry=%+v", entry)
	}
	found, err := db.FindAPIKeyByPlainText(t.Context(), key)
	if err != nil || found.UsedRequests != 0 {
		t.Fatalf("usage=%+v err=%v", found, err)
	}
}
