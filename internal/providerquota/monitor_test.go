package providerquota

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

func TestParseQuotasUsesConfiguredFieldsAndScales(t *testing.T) {
	cfg := minimaxQuotaConfig("https://example.invalid/quota")
	body := []byte(`{
  "model_remains": [
    {
      "model_name": "general",
      "start_time": 1786032000,
      "end_time": 1786042800,
      "remains_time": 7200,
      "current_interval_total_count": 0,
      "current_interval_usage_count": 0,
      "current_interval_remaining_percent": 99,
      "current_interval_status": 1,
      "weekly_start_time": 1785686400,
      "weekly_end_time": 1786291200,
      "weekly_remains_time": 266400,
      "current_weekly_total_count": 0,
      "current_weekly_usage_count": 0,
      "current_weekly_remaining_percent": 94,
      "current_weekly_status": 3,
      "weekly_boost_permille": 1500
    },
    {
      "model_name": "video",
      "current_interval_total_count": 3,
      "current_interval_usage_count": 0,
      "current_interval_remaining_percent": 100,
      "current_interval_status": 2,
      "current_weekly_total_count": 21,
      "current_weekly_usage_count": 0,
      "current_weekly_remaining_percent": 100,
      "current_weekly_status": 1
    }
  ]
}`)

	quotas, err := parseQuotas(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(quotas) != 2 {
		t.Fatalf("quota count = %d", len(quotas))
	}
	general := quotas[0]
	if general.Current.UsedCount == nil || *general.Current.UsedCount != 0 || general.Current.RemainingPercent == nil || *general.Current.RemainingPercent != 99 {
		t.Fatalf("unexpected current quota: %#v", general.Current)
	}
	if general.Weekly.Status != "unlimited" || general.Weekly.RawStatus != float64(3) {
		t.Fatalf("unexpected weekly status: %#v", general.Weekly)
	}
	if general.WeeklyBoostMultiplier == nil || *general.WeeklyBoostMultiplier != 1.5 {
		t.Fatalf("weekly boost = %#v", general.WeeklyBoostMultiplier)
	}
	if quotas[1].Current.Status != "exhausted" || quotas[1].Current.TotalCount == nil || *quotas[1].Current.TotalCount != 3 {
		t.Fatalf("unexpected video quota: %#v", quotas[1])
	}
}

func TestParseQuotasSupportsNestedVendorPathsAndFormats(t *testing.T) {
	cfg := config.ProviderQuotaConfig{
		ItemsPath: "data.limits",
		Fields: map[string]string{
			"model_name":                "identity.name",
			"current_end_time":          "period.reset_at",
			"current_used_count":        "period.consumed",
			"current_remaining_percent": "period.remaining_ratio",
			"current_status":            "period.state",
		},
		Scales: map[string]float64{
			"current_remaining_percent": 100,
		},
		StatusValues: map[string]string{
			"ready": "normal",
		},
	}
	body := []byte(`{"data":{"limits":[{"identity":{"name":"vendor-chat"},"period":{"reset_at":"2026-08-07T00:00:00+08:00","consumed":"12","remaining_ratio":0.75,"state":"ready"}}]}}`)

	quotas, err := parseQuotas(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(quotas) != 1 || quotas[0].ModelName != "vendor-chat" {
		t.Fatalf("unexpected quotas: %#v", quotas)
	}
	current := quotas[0].Current
	if current.UsedCount == nil || *current.UsedCount != 12 || current.RemainingPercent == nil || *current.RemainingPercent != 75 || current.Status != "normal" {
		t.Fatalf("unexpected nested current window: %#v", current)
	}
	if current.ResetAt == nil || current.ResetAt.Format(time.RFC3339) != "2026-08-06T16:00:00Z" {
		t.Fatalf("unexpected reset time: %#v", current.ResetAt)
	}
}

func TestMonitorSweepDeduplicatesAndFansOut(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer sk-shared" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model_remains":[{"model_name":"general","current_interval_usage_count":1,"current_interval_remaining_percent":99,"current_interval_status":1}]}`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	configs := []config.ProviderConfig{
		quotaProvider("minimax", server.URL, "sk-shared"),
		quotaProvider("minimax-anthropic", server.URL, "sk-shared"),
	}
	monitor, err := New(configs, Options{Client: server.Client(), Logger: log.New(io.Discard, "", 0), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	monitor.sweep(t.Context())

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	snapshot := monitor.Snapshot()
	if len(snapshot.Items) != 2 {
		t.Fatalf("snapshot items = %d", len(snapshot.Items))
	}
	for _, item := range snapshot.Items {
		if item.State != "ok" || len(item.Quotas) != 1 || item.LastSuccessAt == nil {
			t.Fatalf("unexpected item: %#v", item)
		}
	}
}

func TestMonitorFailurePreservesLastSuccess(t *testing.T) {
	var mu sync.Mutex
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentStatus := status
		mu.Unlock()
		w.WriteHeader(currentStatus)
		if currentStatus == http.StatusOK {
			_, _ = io.WriteString(w, `{"model_remains":[{"model_name":"general","current_interval_remaining_percent":88}]}`)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	monitor, err := New([]config.ProviderConfig{quotaProvider("minimax", server.URL, "sk-secret-never-returned")}, Options{
		Client: server.Client(),
		Logger: log.New(io.Discard, "", 0),
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor.sweep(t.Context())
	mu.Lock()
	status = http.StatusUnauthorized
	mu.Unlock()
	now = now.Add(10 * time.Minute)
	monitor.sweep(t.Context())

	item := monitor.Snapshot().Items[0]
	if item.State != "error" || item.LastError != "upstream returned HTTP 401" || len(item.Quotas) != 1 || item.LastSuccessAt == nil {
		t.Fatalf("unexpected stale snapshot: %#v", item)
	}
	if strings.Contains(item.LastError, "secret") {
		t.Fatal("snapshot leaked API key")
	}
}

func TestMonitorReportsResponseAndTimeoutFailures(t *testing.T) {
	tests := []struct {
		name             string
		handler          http.HandlerFunc
		requestTimeout   time.Duration
		maxResponseBytes int64
		wantError        string
	}{
		{
			name: "invalid json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{not-json`)
			},
			wantError: "invalid JSON response",
		},
		{
			name: "missing mapped array",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{}`)
			},
			wantError: `items_path "model_remains" was not found`,
		},
		{
			name: "response too large",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", 65))
			},
			maxResponseBytes: 64,
			wantError:        "upstream response exceeded configured limit",
		},
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(200 * time.Millisecond):
				}
			},
			requestTimeout: 20 * time.Millisecond,
			wantError:      "upstream request timed out",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			monitor, err := New([]config.ProviderConfig{quotaProvider("minimax", server.URL, "sk-secret")}, Options{
				Client:           server.Client(),
				Logger:           log.New(io.Discard, "", 0),
				RequestTimeout:   test.requestTimeout,
				MaxResponseBytes: test.maxResponseBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			monitor.sweep(t.Context())
			item := monitor.Snapshot().Items[0]
			if item.State != "error" || item.LastError != test.wantError {
				t.Fatalf("unexpected error snapshot: %#v", item)
			}
		})
	}
}

func TestMonitorReplaceDropsRemovedAndQueriesChangedTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model_remains":[]}`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	monitor, err := New([]config.ProviderConfig{quotaProvider("minimax", server.URL, "sk-old")}, Options{Client: server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	monitor.sweep(t.Context())
	if err := monitor.Replace([]config.ProviderConfig{quotaProvider("replacement", server.URL, "sk-new")}); err != nil {
		t.Fatal(err)
	}
	snapshot := monitor.Snapshot()
	if len(snapshot.Items) != 1 || snapshot.Items[0].Provider != "replacement" || snapshot.Items[0].State != "pending" {
		t.Fatalf("unexpected replacement snapshot: %#v", snapshot)
	}
	monitor.sweep(t.Context())
	if got := monitor.Snapshot().Items[0].State; got != "ok" {
		t.Fatalf("replacement state = %q", got)
	}
}

func quotaProvider(name, url, apiKey string) config.ProviderConfig {
	quota := minimaxQuotaConfig(url)
	return config.ProviderConfig{
		Name:     name,
		Protocol: "openai",
		BaseURL:  "https://api.example.invalid",
		Quota:    &quota,
		Models:   []config.ModelConfig{{Public: name + "-model", Upstream: "upstream"}},
		Pools: []config.PoolConfig{{
			Name: "primary",
			Accounts: []config.AccountConfig{{
				Name:   "main",
				APIKey: apiKey,
			}},
		}},
	}
}

func minimaxQuotaConfig(url string) config.ProviderQuotaConfig {
	return config.ProviderQuotaConfig{
		URL:       url,
		Interval:  "10m",
		ItemsPath: "model_remains",
		Fields: map[string]string{
			"model_name":                "model_name",
			"current_start_time":        "start_time",
			"current_end_time":          "end_time",
			"current_remaining_seconds": "remains_time",
			"current_total_count":       "current_interval_total_count",
			"current_used_count":        "current_interval_usage_count",
			"current_remaining_percent": "current_interval_remaining_percent",
			"current_status":            "current_interval_status",
			"weekly_start_time":         "weekly_start_time",
			"weekly_end_time":           "weekly_end_time",
			"weekly_remaining_seconds":  "weekly_remains_time",
			"weekly_total_count":        "current_weekly_total_count",
			"weekly_used_count":         "current_weekly_usage_count",
			"weekly_remaining_percent":  "current_weekly_remaining_percent",
			"weekly_status":             "current_weekly_status",
			"weekly_boost_multiplier":   "weekly_boost_permille",
		},
		Scales: map[string]float64{
			"weekly_boost_multiplier": 0.001,
		},
		StatusValues: map[string]string{
			"1": "normal",
			"2": "exhausted",
			"3": "unlimited",
		},
	}
}
