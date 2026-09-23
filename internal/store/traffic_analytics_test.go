package store

import (
	"testing"
	"time"
)

func TestTrafficAnalyticsFiltersAndTotals(t *testing.T) {
	s, telemetry := openReportingStores(t)
	a, err := s.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	for i := 0; i < 125; i++ {
		enqueueRequestLogForModelReportingTest(t, telemetry, a.APIKey, "model-a", at.Add(time.Duration(i)*time.Second), 200, 2, 3, 1, 1, 10000, "")
	}
	enqueueRequestLogForModelReportingTest(t, telemetry, a.APIKey, "model-a", at.Add(24*time.Hour), 200, 7, 8, 0, 0, 20000, "")
	enqueueRequestLogForModelReportingTest(t, telemetry, b.APIKey, "model-b", at, 502, 4, 6, 0, 0, 30000, "upstream")
	enqueueRequestLogForModelReportingTest(t, telemetry, b.APIKey, "model-b", at, 200, 4, 6, 0, 0, 30000, "stream_error")
	result, err := s.TrafficAnalytics(t.Context(), TrafficQuery{Bucket: "day", TimezoneOffset: 480})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Requests != 128 || result.Summary.UniqueAPIKeys != 2 || result.Summary.CostMicro != 1330000 || result.Summary.ErrorRequests != 2 {
		t.Fatalf("bad summary: %+v", result.Summary)
	}
	if len(result.Items) != 2 || result.Items[0].Bucket != "2026-09-02" || result.Items[1].Bucket != "2026-09-03" {
		t.Fatalf("bad buckets: %+v", result.Items)
	}
	if len(result.Models) != 2 || result.Models[0].Requests != 126 || result.Models[0].TotalTokens != 640 || len(result.Keys) != 2 {
		t.Fatalf("bad rankings: %+v", result)
	}
	end := at.Add(24 * time.Hour)
	filters := TrafficFilters{APIKeyIDs: []string{a.ID, b.ID}, Models: []string{"model-a"}, Provider: "provider-a", Status: "success", ExclusiveEnd: true}
	query := TrafficQuery{From: &at, To: &end, Filters: filters, TimezoneOffset: 480}
	filtered, err := s.TrafficAnalytics(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Summary.Requests != 125 || filtered.Summary.UniqueAPIKeys != 1 || filtered.Summary.TotalTokens != 625 || filtered.Summary.CostMicro != 1250000 {
		t.Fatalf("bad filtered summary: %+v", filtered.Summary)
	}
	if len(filtered.Options.Keys) != 2 || len(filtered.Options.Models) != 2 {
		t.Fatal("filter options must not shrink")
	}
	logs, err := s.ListRequestLogs(t.Context(), RequestLogQuery{From: &at, To: &end, Filters: filters, Limit: 50, Offset: 100})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 125 || len(logs.Items) != 25 {
		t.Fatalf("bad log pagination: %+v", logs)
	}
	failed, err := s.TrafficAnalytics(t.Context(), TrafficQuery{Filters: TrafficFilters{Status: "failed"}})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Summary.Requests != 2 || failed.Summary.ErrorRequests != 2 {
		t.Fatalf("bad failures: %+v", failed.Summary)
	}
	empty, err := s.TrafficAnalytics(t.Context(), TrafficQuery{Filters: TrafficFilters{Models: []string{"' OR 1=1 --"}}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Summary.Requests != 0 || len(empty.Items) != 0 || len(empty.Models) != 0 {
		t.Fatal("expected empty exact-match filter")
	}
}

func TestTrafficAnalyticsExclusiveBoundaryAndTimezone(t *testing.T) {
	s, telemetry := openReportingStores(t)
	key, err := s.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for _, at := range []time.Time{from.Add(-time.Second), from, from.Add(123 * time.Millisecond), to.Add(-time.Second), to} {
		enqueueRequestLogForReportingTest(t, telemetry, key.APIKey, at, 200, 1, 1, 0, 0, 0, "")
	}
	result, err := s.TrafficAnalytics(t.Context(), TrafficQuery{From: &from, To: &to, Bucket: "month", TimezoneOffset: 480, Filters: TrafficFilters{ExclusiveEnd: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Requests != 3 || len(result.Items) != 1 || result.Items[0].Bucket != "2026-09" {
		t.Fatalf("wrong boundary result: %+v", result)
	}
}
