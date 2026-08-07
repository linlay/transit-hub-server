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

func TestParseEntriesRendersConfiguredText(t *testing.T) {
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

	entries, err := parseEntries(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d", len(entries))
	}
	general := entries[0]
	if general.Title != "general" {
		t.Fatalf("general title = %q", general.Title)
	}
	wantGeneral := []string{
		"当前窗口：已用量 0，剩余 99%",
		"当前重置：2026-08-07 03:00:00 +08:00，剩余 2 小时",
		"当前状态：正常",
		"本周：已用量 0，剩余 94%",
		"周重置：2026-08-10 00:00:00 +08:00，剩余 3 天 2 小时",
		"周状态：无限",
		"周额度加成：1.5 倍",
	}
	if strings.Join(general.Lines, "\n") != strings.Join(wantGeneral, "\n") {
		t.Fatalf("general lines:\n%q\nwant:\n%q", general.Lines, wantGeneral)
	}
	video := entries[1]
	if video.Title != "video" || len(video.Lines) != 6 {
		t.Fatalf("unexpected video entry: %#v", video)
	}
	if video.Lines[1] != "当前计数：已用 0 / 3" || video.Lines[2] != "当前状态：已耗尽" || video.Lines[4] != "周计数：已用 0 / 21" || video.Lines[5] != "周状态：正常" {
		t.Fatalf("unexpected mapped video states: %#v", video.Lines)
	}
}

func TestParseEntriesSupportsNestedRootPathsAndFormats(t *testing.T) {
	cfg := config.ProviderQuotaConfig{
		ItemsPath: "data.limits",
		Fields: map[string]string{
			"plan":      "$root.data.plan.name",
			"name":      "identity.name",
			"reset":     "period.reset_at",
			"consumed":  "period.consumed",
			"remaining": "period.remaining_ratio",
			"optional":  "period.not_present",
		},
		Display: config.ProviderQuotaDisplayConfig{
			Title: "{{ plan }} / {{ name }}",
			Lines: []string{
				"已用 {{ consumed | number:0 }}，剩余 {{ remaining | scale:100 | percent:1 }}",
				"重置：{{ reset | datetime:Asia/Shanghai }}",
				"这一行会隐藏：{{ optional }}",
			},
		},
	}
	body := []byte(`{"data":{"plan":{"name":"Developer Plan"},"limits":[{"identity":{"name":"vendor-chat"},"period":{"reset_at":"2026-08-07T00:00:00+08:00","consumed":"12","remaining_ratio":0.75}}]}}`)

	entries, err := parseEntries(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Title != "Developer Plan / vendor-chat" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	wantLines := []string{"已用 12，剩余 75%", "重置：2026-08-07 00:00:00 +08:00"}
	if strings.Join(entries[0].Lines, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("unexpected lines: %#v", entries[0].Lines)
	}
}

func TestParseEntriesSupportsBalanceInsteadOfTimeWindows(t *testing.T) {
	cfg := config.ProviderQuotaConfig{
		ItemsPath: "balance_infos",
		Fields: map[string]string{
			"available": "$root.is_available",
			"currency":  "currency",
			"total":     "total_balance",
			"granted":   "granted_balance",
			"topped_up": "topped_up_balance",
		},
		Display: config.ProviderQuotaDisplayConfig{
			Title: "{{ currency | map:currency_name }}余额",
			Lines: []string{
				"可用余额：{{ currency | map:currency_symbol }}{{ total | number:2 }}",
				"账户状态：{{ available | map:availability }}",
				"充值余额：{{ currency | map:currency_symbol }}{{ topped_up | number:2 }}",
				"赠送余额：{{ currency | map:currency_symbol }}{{ granted | number:2 }}",
			},
			ValueMaps: map[string]map[string]string{
				"availability":    {"true": "可用", "false": "不可用"},
				"currency_name":   {"CNY": "人民币", "USD": "美元"},
				"currency_symbol": {"CNY": "¥", "USD": "$"},
			},
		},
	}
	body := []byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"18.50","granted_balance":"3.50","topped_up_balance":"15.00"}]}`)

	entries, err := parseEntries(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{
		Title: "人民币余额",
		Lines: []string{
			"可用余额：¥18.5",
			"账户状态：可用",
			"充值余额：¥15",
			"赠送余额：¥3.5",
		},
	}
	if len(entries) != 1 || entries[0].Title != want.Title || strings.Join(entries[0].Lines, "\n") != strings.Join(want.Lines, "\n") {
		t.Fatalf("unexpected balance entry: %#v", entries)
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
		if item.State != "ok" || len(item.Entries) != 1 || item.LastSuccessAt == nil {
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
			_, _ = io.WriteString(w, `{"model_remains":[{"model_name":"general","current_interval_usage_count":12,"current_interval_remaining_percent":88}]}`)
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
	if item.State != "error" || item.LastError != "upstream returned HTTP 401" || len(item.Entries) != 1 || item.LastSuccessAt == nil {
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

func TestMonitorReplacePreservesUnchangedCacheAndInvalidatesDisplayChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model_remains":[{"model_name":"general","current_interval_usage_count":1,"current_interval_remaining_percent":99}]}`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	original := quotaProvider("minimax", server.URL, "sk-stable")
	monitor, err := New([]config.ProviderConfig{original}, Options{Client: server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	monitor.sweep(t.Context())
	if got := monitor.Snapshot().Items[0].State; got != "ok" {
		t.Fatalf("initial state = %q", got)
	}

	unchanged := quotaProvider("minimax", server.URL, "sk-stable")
	if err := monitor.Replace([]config.ProviderConfig{unchanged}); err != nil {
		t.Fatal(err)
	}
	if got := monitor.Snapshot().Items[0].State; got != "ok" {
		t.Fatalf("unchanged state = %q", got)
	}

	changed := quotaProvider("minimax", server.URL, "sk-stable")
	changed.Quota.Display.Lines[0] = "更新后：{{ current_remaining | percent:1 }}"
	if err := monitor.Replace([]config.ProviderConfig{changed}); err != nil {
		t.Fatal(err)
	}
	if item := monitor.Snapshot().Items[0]; item.State != "pending" || len(item.Entries) != 0 {
		t.Fatalf("changed display should invalidate cache: %#v", item)
	}
	monitor.sweep(t.Context())
	item := monitor.Snapshot().Items[0]
	if item.State != "ok" || item.Entries[0].Lines[0] != "更新后：99%" {
		t.Fatalf("changed display was not rendered: %#v", item)
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
			"current_used":              "current_interval_usage_count",
			"current_total":             "current_interval_total_count",
			"current_remaining":         "current_interval_remaining_percent",
			"current_reset":             "end_time",
			"current_remaining_seconds": "remains_time",
			"current_status":            "current_interval_status",
			"weekly_used":               "current_weekly_usage_count",
			"weekly_total":              "current_weekly_total_count",
			"weekly_remaining":          "current_weekly_remaining_percent",
			"weekly_reset":              "weekly_end_time",
			"weekly_remaining_seconds":  "weekly_remains_time",
			"weekly_status":             "current_weekly_status",
			"weekly_boost":              "weekly_boost_permille",
		},
		Display: config.ProviderQuotaDisplayConfig{
			Title: "{{ model_name }}",
			Lines: []string{
				"当前窗口：已用量 {{ current_used | number:0 }}，剩余 {{ current_remaining | percent:1 }}",
				"当前计数：已用 {{ current_used | number:0 }} / {{ current_total | nonzero | number:0 }}",
				"当前重置：{{ current_reset | datetime:Asia/Shanghai }}，剩余 {{ current_remaining_seconds | duration:zh-CN }}",
				"当前状态：{{ current_status | map:quota_status }}",
				"本周：已用量 {{ weekly_used | number:0 }}，剩余 {{ weekly_remaining | percent:1 }}",
				"周计数：已用 {{ weekly_used | number:0 }} / {{ weekly_total | nonzero | number:0 }}",
				"周重置：{{ weekly_reset | datetime:Asia/Shanghai }}，剩余 {{ weekly_remaining_seconds | duration:zh-CN }}",
				"周状态：{{ weekly_status | map:quota_status }}",
				"周额度加成：{{ weekly_boost | scale:0.001 | number:2 }} 倍",
			},
			ValueMaps: map[string]map[string]string{
				"quota_status": {
					"1": "正常",
					"2": "已耗尽",
					"3": "无限",
				},
			},
		},
	}
}
