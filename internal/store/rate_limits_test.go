package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRateLimitStatusesIgnoreLogsOutsideFixedWindow(t *testing.T) {
	control := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	defer closeTestStore(t, control)

	created, err := control.CreateAPIKey(t.Context(), CreateAPIKeyParams{
		Name:       "windowed",
		RateLimits: []RateLimit{{Window: RateLimitWindow1H, RequestQuota: 10, TokenQuota: 100, CostQuotaMicro: 1000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageManager(filepath.Join(t.TempDir(), "usage.db"), loc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := usage.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
	control.AttachRuntime(usage, nil)

	now := time.Date(2026, 6, 12, 15, 30, 0, 0, loc)
	outside := time.Date(2026, 6, 12, 14, 59, 0, 0, loc)
	inside := time.Date(2026, 6, 12, 15, 1, 0, 0, loc)
	usage.Record(created.ID, 5, 5, 100, outside)
	usage.Record(created.ID, 3, 4, 11, inside)

	statuses, err := control.RateLimitStatuses(t.Context(), created.ID, created.RateLimits, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses len = %d", len(statuses))
	}
	status := statuses[0]
	if status.Requests != 1 || status.Tokens != 7 || status.CostMicro != 11 {
		t.Fatalf("unexpected status usage: %#v", status)
	}
	if status.RequestRemaining != 9 || status.TokenRemaining != 93 || status.CostRemainingMicro != 989 {
		t.Fatalf("unexpected remaining values: %#v", status)
	}
}
