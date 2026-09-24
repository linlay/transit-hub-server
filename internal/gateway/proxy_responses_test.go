package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func TestResponsesProxyPassthroughAndBilling(t *testing.T) {
	usageJSON := `"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":10}}`
	final := `{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque+/=="}],` + usageJSON + `}`
	for _, tc := range []struct {
		name, body, contentType, billing string
		status                           int
		stream, override                 bool
		tokens, cost                     int64
	}{
		{"json", final, "application/json", "charged", 200, false, false, 120, 120},
		{"sse", "event: response.completed\ndata: " + `{"type":"response.completed","response":` + final + "}\n\n", "text/event-stream", "charged", 200, true, true, 120, 120},
		{"zero", `{"usage":{"input_tokens":0,"output_tokens":0}}`, "application/json", "charged", 200, false, false, 0, 0},
		{"missing", `{"id":"resp_1","output":[]}`, "application/json", "unavailable", 200, false, false, 0, 0},
		{"failed_event", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\"}}}\n\n", "text/event-stream", "unavailable", 200, true, false, 0, 0},
		{"upstream_error", `{"error":{"code":"invalid_encrypted_content","message":"invalid state"}}`, "application/json", "not_charged", 400, true, false, 0, 0},
		{"oversized_event", "data: {\"type\":\"response.completed\",\"response\":{\"padding\":\"" + strings.Repeat("x", responseSampleLimit) + "\"," + usageJSON + "}}\n\n", "text/event-stream", "unavailable", 200, true, false, 0, 0},
		{"late_usage", strings.Repeat(": keepalive\n\n", responseSampleLimit/13+1) + "data: {\"type\":\"response.completed\",\"response\":" + final + "}\n\n", "text/event-stream", "charged", 200, true, false, 120, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestBody := fmt.Sprintf(`{"model":"public-model","stream":%t,"store":false,"include":["reasoning.encrypted_content"],"reasoning":{"effort":"medium","summary":"auto"},"max_output_tokens":4096,"input":[{"type":"reasoning","id":"rs_old","encrypted_content":"opaque+/=="},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"hello"},{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}],"tools":[{"type":"function","name":"read","parameters":{"type":"object"},"strict":false}]}`, tc.stream)
			var expected map[string]any
			if err := json.Unmarshal([]byte(requestBody), &expected); err != nil {
				t.Fatal(err)
			}
			expected["model"] = "upstream-model"
			path := "/v1/responses"
			if tc.override {
				path = "/custom/responses"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != path || r.URL.RawQuery != "test=1" {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("X-Api-Key") != "" {
					t.Error("incorrect upstream auth")
				}
				var actual map[string]any
				if err := json.NewDecoder(r.Body).Decode(&actual); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Errorf("request changed: %#v", actual)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-Id", "request_1")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			cfg := openAIProvider(upstream.URL)
			if tc.override {
				cfg.Endpoints = map[string]string{"openai_responses": path}
			}
			app, db, plain := newTestGateway(t, []config.ProviderConfig{cfg})
			hitPrice := int64(500_000)
			_, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: "openai", PublicModel: "public-model", Currency: "CNY", InputCostMicroPer1MTokens: 1_000_000, OutputCostMicroPer1MTokens: 2_000_000, InputCacheHitCostMicroPer1MTokens: &hitPrice})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/responses?test=1", strings.NewReader(requestBody))
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("X-Api-Key", plain)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Body.String() != tc.body || rec.Header().Get("X-Request-Id") != "request_1" || rec.Header().Get("Content-Type") != tc.contentType {
				t.Fatal("response was not passed through")
			}
			if tc.stream && !rec.Flushed {
				t.Error("SSE not flushed")
			}
			key, err := db.FindAPIKeyByPlainText(t.Context(), plain)
			if err != nil {
				t.Fatal(err)
			}
			if key.UsedRequests != 1 || key.UsedTokens != tc.tokens || key.UsedCostMicro != tc.cost {
				t.Fatalf("usage: requests=%d tokens=%d cost=%d", key.UsedRequests, key.UsedTokens, key.UsedCostMicro)
			}
			if err := app.telemetry.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Items) != 1 || logs.Items[0].BillingStatus != tc.billing || logs.Items[0].Estimated {
				t.Fatalf("logs: %+v", logs.Items)
			}
		})
	}
}

func TestResponsesRejectsNonChatAndMissingAuth(t *testing.T) {
	for _, modelType := range []string{config.ModelTypeEmbedding, config.ModelTypeImageGeneration} {
		t.Run(modelType, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") }))
			defer upstream.Close()
			cfg := openAIProvider(upstream.URL)
			cfg.Models[0].Type = modelType
			app, _, plain := newTestGateway(t, []config.ProviderConfig{cfg})
			for _, auth := range []bool{false, true} {
				req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public-model","input":"hello"}`))
				want := http.StatusUnauthorized
				if auth {
					req.Header.Set("Authorization", "Bearer "+plain)
					want = http.StatusBadRequest
				}
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != want {
					t.Fatalf("status: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}
