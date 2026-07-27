package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficSupportsMonthBuckets(t *testing.T) {
	store, telemetry := openReportingStores(t)

	key, err := store.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "monthly"})
	if err != nil {
		t.Fatal(err)
	}

	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), 200, 10, 20, 3, 7, 111, "")
	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), 502, 5, 8, 2, 1, 222, "upstream")
	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 200, 4, 6, 0, 0, 333, "")

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
	store, telemetry := openReportingStores(t)

	key, err := store.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "overview"})
	if err != nil {
		t.Fatal(err)
	}

	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), 200, 10, 20, 3, 7, 111, "")
	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), 502, 5, 8, 2, 1, 222, "upstream")
	enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 200, 4, 6, 0, 0, 333, "")

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

func openReportingStores(t *testing.T) (*Store, *Telemetry) {
	t.Helper()
	root := t.TempDir()
	control := openTestStore(t, filepath.Join(root, "control.db"))
	telemetry, err := NewTelemetry(filepath.Join(root, "telemetry.db"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	control.AttachRuntime(nil, telemetry)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Close(ctx); err != nil {
			t.Fatal(err)
		}
		closeTestStore(t, control)
	})
	return control, telemetry
}

func enqueueRequestLogForReportingTest(t *testing.T, telemetry *Telemetry, key APIKey, createdAt time.Time, statusCode int, requestTokens, responseTokens, cacheHitTokens, cacheMissTokens, costMicro int64, errorType string) {
	t.Helper()
	if !telemetry.Enqueue(RequestLog{
		APIKeyID:        key.ID,
		APIKeyName:      key.Name,
		KeyPrefix:       key.KeyPrefix,
		Protocol:        "openai",
		PublicModel:     "public-model",
		UpstreamModel:   "upstream-model",
		Provider:        "provider-a",
		Pool:            "default",
		Account:         "acct",
		StatusCode:      statusCode,
		Latency:         10 * time.Millisecond,
		RequestTokens:   requestTokens,
		ResponseTokens:  responseTokens,
		CacheHitTokens:  cacheHitTokens,
		CacheMissTokens: cacheMissTokens,
		CostMicro:       costMicro,
		ErrorType:       errorType,
		CreatedAt:       createdAt,
	}) {
		t.Fatal("telemetry queue unexpectedly full")
	}
}
