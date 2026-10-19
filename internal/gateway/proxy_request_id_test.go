package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func TestProxyUpstreamRequestID(t *testing.T) {
	for _, tc := range []struct {
		name, path, configured, custom, native, want string
		status                                       int
		stream                                       bool
	}{
		{"chat", "/v1/chat/completions", "X-Babelark-Request-Id", "babel-1", "", "babel-1", 200, false},
		{"chat_stream", "/v1/chat/completions", "x-babelark-request-id", "babel-2", "", "babel-2", 200, true},
		{"responses_stream", "/v1/responses", "X-Babelark-Request-Id", "babel-3", "", "babel-3", 200, true},
		{"anthropic", "/v1/messages", "X-Babelark-Request-Id", "babel-4", "", "babel-4", 200, false},
		{"forbidden", "/v1/chat/completions", "X-Babelark-Request-Id", "babel-403", "", "babel-403", 403, false},
		{"server_error", "/v1/responses", "X-Babelark-Request-Id", "babel-500", "", "babel-500", 500, false},
		{"priority", "/v1/responses", "X-Babelark-Request-Id", "babel", "native", "babel", 200, false},
		{"fallback", "/v1/responses", "X-Babelark-Request-Id", "", "native", "native", 200, false},
		{"empty_custom", "/v1/responses", "X-Babelark-Request-Id", "  ", "native", "native", 200, false},
		{"unconfigured", "/v1/responses", "", "babel", "native", "native", 200, false},
		{"native_source", "/v1/responses", "x-request-id", "", "native", "native", 200, false},
		{"other_source", "/v1/responses", "X-Vendor-Trace", "vendor-trace", "", "vendor-trace", 200, false},
		{"missing", "/v1/responses", "X-Babelark-Request-Id", "", "", "", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"usage":{"input_tokens":1,"output_tokens":1}}`
			if tc.stream {
				body = "data: " + body + "\n\n"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.custom != "" {
					sourceHeader := tc.configured
					if sourceHeader == "" {
						sourceHeader = "X-Babelark-Request-Id"
					}
					w.Header().Set(sourceHeader, tc.custom)
				}
				if tc.native != "" {
					w.Header().Set("X-Request-Id", tc.native)
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(body))
			}))
			defer upstream.Close()
			cfg := openAIProvider(upstream.URL)
			if tc.path == "/v1/messages" {
				cfg = anthropicProvider(upstream.URL)
			}
			cfg.RequestIDHeader = tc.configured
			app, db, plain := newTestGateway(t, []config.ProviderConfig{cfg})
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"input":"hi","max_tokens":16,"stream":%t}`, cfg.Models[0].Public, tc.stream)))
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:5173")
			req.Header.Set("X-Request-Id", "client-supplied-not-an-upstream-id")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			// Result captures headers at WriteHeader, before streaming starts.
			response := rec.Result()
			defer response.Body.Close()
			if rec.Code != tc.status || rec.Body.String() != body {
				t.Fatalf("response changed: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := response.Header.Get("X-Request-Id"); got != tc.want || len(response.Header.Values("X-Request-Id")) > 1 {
				t.Fatalf("request ID headers=%v want=%q", response.Header.Values("X-Request-Id"), tc.want)
			}
			if tc.configured != "" && !strings.EqualFold(tc.configured, "X-Request-Id") && len(response.Header.Values(tc.configured)) != 0 {
				t.Fatalf("upstream request ID source header leaked: %s", tc.configured)
			}
			if tc.configured == "" && response.Header.Get("X-Babelark-Request-Id") != tc.custom {
				t.Fatal("unconfigured response header was removed")
			}
			if !strings.Contains(strings.Join(response.Header.Values("Access-Control-Expose-Headers"), ","), "X-Request-Id") {
				t.Fatal("request ID not exposed to browsers")
			}
			if tc.stream && !rec.Flushed {
				t.Fatal("stream not flushed")
			}
			if err := app.telemetry.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Items) != 1 || logs.Items[0].UpstreamRequestID != tc.want {
				t.Fatalf("logs=%+v", logs.Items)
			}
			if tc.status >= 400 && logs.Items[0].ErrorType != "upstream_status" {
				t.Fatal("upstream error not diagnosed")
			}
		})
	}
}

func TestProxyRequestIDOnInterruptedStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Babelark-Request-Id", "interrupted-stream")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("data: partial\n\n"))
	}))
	defer upstream.Close()
	cfg := openAIProvider(upstream.URL)
	cfg.RequestIDHeader = "X-Babelark-Request-Id"
	app, db, plain := newTestGateway(t, []config.ProviderConfig{cfg})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if got := rec.Result().Header.Get("X-Request-Id"); got != "interrupted-stream" {
		t.Fatalf("request ID=%q", got)
	}
	if len(rec.Result().Header.Values("X-Babelark-Request-Id")) != 0 {
		t.Fatal("upstream request ID source header leaked on interrupted stream")
	}
	if err := app.telemetry.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Items) != 1 || logs.Items[0].UpstreamRequestID != "interrupted-stream" || logs.Items[0].ErrorType != "response_copy_error" {
		t.Fatalf("interrupted stream diagnosis=%+v", logs.Items)
	}
}
