package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficSupportsMonthBuckets(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "reporting.db"))
	defer closeTestStore(t, store)

	key, err := store.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "monthly"})
	if err != nil {
		t.Fatal(err)
	}

	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), 200, 10, 20, 3, 7, 111, "")
	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), 502, 5, 8, 2, 1, 222, "upstream")
	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 200, 4, 6, 0, 0, 333, "")

	traffic, err := store.Traffic(t.Context(), TrafficQuery{APIKeyID: key.ID, Bucket: "month"})
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 2 {
		t.Fatalf("traffic len = %d: %#v", len(traffic), traffic)
	}

	jan := traffic[0]
	if jan.Bucket != "2026-01" || jan.Requests != 2 || jan.RequestTokens != 15 || jan.ResponseTokens != 28 || jan.TotalTokens != 43 {
		t.Fatalf("unexpected January traffic totals: %#v", jan)
	}
	if jan.CacheHitTokens != 5 || jan.CacheMissTokens != 8 || jan.CacheTotalTokens != 13 {
		t.Fatalf("unexpected January cache totals: %#v", jan)
	}
	if jan.CostMicro != 333 || jan.ErrorRequests != 1 {
		t.Fatalf("unexpected January cost/error totals: %#v", jan)
	}
	if jan.CacheHitRate == nil || *jan.CacheHitRate < 0.3846 || *jan.CacheHitRate > 0.3847 {
		t.Fatalf("unexpected January cache hit rate: %#v", jan.CacheHitRate)
	}

	feb := traffic[1]
	if feb.Bucket != "2026-02" || feb.Requests != 1 || feb.TotalTokens != 10 || feb.CostMicro != 333 || feb.ErrorRequests != 0 {
		t.Fatalf("unexpected February traffic totals: %#v", feb)
	}
}

func TestOverviewSupportsTimeRange(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "overview.db"))
	defer closeTestStore(t, store)

	key, err := store.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "overview"})
	if err != nil {
		t.Fatal(err)
	}

	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), 200, 10, 20, 3, 7, 111, "")
	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), 502, 5, 8, 2, 1, 222, "upstream")
	insertRequestLogForReportingTest(t, store, key.ID, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 200, 4, 6, 0, 0, 333, "")

	all, err := store.Overview(t.Context(), 5*time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if all.TotalRequests != 3 || all.RequestTokens != 19 || all.ResponseTokens != 34 || all.TotalTokens != 53 || all.TotalCost != 666 || all.ErrorRequests != 1 {
		t.Fatalf("unexpected all-time overview totals: %#v", all)
	}

	from := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	ranged, err := store.Overview(t.Context(), 5*time.Minute, &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	if ranged.TotalRequests != 1 || ranged.RequestTokens != 5 || ranged.ResponseTokens != 8 || ranged.TotalTokens != 13 || ranged.TotalCost != 222 || ranged.ErrorRequests != 1 {
		t.Fatalf("unexpected ranged overview totals: %#v", ranged)
	}
	if ranged.AverageLatency != 10 {
		t.Fatalf("unexpected ranged average latency: %f", ranged.AverageLatency)
	}
	if len(ranged.RecentTraffic) != 1 || ranged.RecentTraffic[0].Bucket != "2026-01-31" || ranged.RecentTraffic[0].Requests != 1 {
		t.Fatalf("unexpected ranged recent traffic: %#v", ranged.RecentTraffic)
	}

	fullFrom := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	fullTo := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	fullRange, err := store.Overview(t.Context(), 5*time.Minute, &fullFrom, &fullTo)
	if err != nil {
		t.Fatal(err)
	}
	if fullRange.TotalRequests != all.TotalRequests || fullRange.TotalTokens != all.TotalTokens || fullRange.TotalCost != all.TotalCost || fullRange.ErrorRequests != all.ErrorRequests {
		t.Fatalf("full-range overview does not match all-time: all=%#v fullRange=%#v", all, fullRange)
	}
}

func insertRequestLogForReportingTest(t *testing.T, store *Store, apiKeyID string, createdAt time.Time, statusCode int, requestTokens, responseTokens, cacheHitTokens, cacheMissTokens, costMicro int64, errorType string) {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), `
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			status_code, latency_ms, request_tokens, response_tokens, cache_hit_tokens,
			cache_miss_tokens, cost_micro, error_type, created_at
		) VALUES (?, 'openai', 'public-model', 'upstream-model', 'provider-a', 'default', 'acct', ?, 10, ?, ?, ?, ?, ?, ?, ?)
	`, apiKeyID, statusCode, requestTokens, responseTokens, cacheHitTokens, cacheMissTokens, costMicro, errorType, formatTime(createdAt.UTC()))
	if err != nil {
		t.Fatal(err)
	}
}
