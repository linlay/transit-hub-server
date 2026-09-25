package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func independentManager(t *testing.T) (*UsageManager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	u, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close(context.Background()) })
	return u, path
}
func windowTestKey(id string) APIKey {
	return APIKey{ID: id, Status: "active", RateLimits: []RateLimit{{Window: "5h", RequestQuota: 10}, {Window: "7d", RequestQuota: 20}}}
}
func admitTest(t *testing.T, u *UsageManager, key APIKey, at time.Time) WindowBindings {
	t.Helper()
	b, err := u.Admit(t.Context(), key, at)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func statusTest(t *testing.T, u *UsageManager, key APIKey, at time.Time) []RateLimitStatus {
	t.Helper()
	s, err := u.RateLimitStatuses(key.ID, key.RateLimits, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIndependentWindowsFirstUseExpiryAndLateCompletion(t *testing.T) {
	u, _ := independentManager(t)
	key := windowTestKey("a")
	at := time.Now().UTC()
	for _, s := range statusTest(t, u, key, at) {
		if s.State != "idle" || !s.StartsAt.IsZero() {
			t.Fatalf("read started window: %+v", s)
		}
	}
	first := admitTest(t, u, key, at)
	other := admitTest(t, u, windowTestKey("b"), at.Add(time.Hour))
	if first["5h"].Equal(other["5h"]) {
		t.Fatal("keys share start")
	}
	u.Record(key.ID, 2, 3, 7, at, first)
	expired := statusTest(t, u, key, at.Add(5*time.Hour))
	if expired[0].State != "expired" || expired[0].Requests != 0 || expired[1].Requests != 1 {
		t.Fatalf("expiry: %+v", expired)
	}
	nextTime := at.Add(12 * time.Hour)
	second := admitTest(t, u, key, nextTime)
	if !second["5h"].Equal(nextTime) || !second["7d"].Equal(at) {
		t.Fatalf("independence: %+v", second)
	}
	u.Record(key.ID, 5, 6, 9, at, first) // old long-running request finishes after rotation
	u.Record(key.ID, 1, 1, 2, nextTime, second)
	s := statusTest(t, u, key, nextTime)
	if s[0].Requests != 1 || s[0].Tokens != 2 || s[1].Requests != 3 || u.Total(key.ID).UsedRequests != 3 {
		t.Fatalf("late completion: %+v", s)
	}
	if !s[1].ResetsAt.Equal(at.Add(168 * time.Hour)) {
		t.Fatal("week is not 168 hours")
	}
	afterWeek := at.Add(9 * 24 * time.Hour)
	third := admitTest(t, u, key, afterWeek)
	if !third["7d"].Equal(afterWeek) {
		t.Fatal("idle week auto-renewed")
	}
}

func TestIndependentWindowQuotaBlocksOtherWindowCreation(t *testing.T) {
	for _, dimension := range []string{"requests", "tokens", "cost"} {
		t.Run(dimension, func(t *testing.T) {
			u, _ := independentManager(t)
			key := windowTestKey("blocked")
			key.RateLimits = []RateLimit{{Window: "5h", RequestQuota: 10}, {Window: "7d"}}
			switch dimension {
			case "requests":
				key.RateLimits[1].RequestQuota = 1
			case "tokens":
				key.RateLimits[1].TokenQuota = 2
			case "cost":
				key.RateLimits[1].QuotaMicrocredits = 3
			}
			at := time.Now().UTC()
			first := admitTest(t, u, key, at)
			u.Record(key.ID, 1, 1, 3, at, first)
			_, err := u.Admit(t.Context(), key, at.Add(6*time.Hour))
			var v RateLimitViolation
			if !errors.As(err, &v) || v.Window != "7d" || v.Dimension != dimension {
				t.Fatalf("violation: %v", err)
			}
			if !u.windows[windowKey{key.ID, "5h"}].Start.Equal(at) {
				t.Fatal("rejected request reopened 5h")
			}
		})
	}
}

func TestIndependentWindowsPersistConcurrentFirstUse(t *testing.T) {
	u, path := independentManager(t)
	key := windowTestKey("concurrent")
	key.RateLimits[0].RequestQuota = 100
	key.RateLimits[1].RequestQuota = 100
	at := time.Now().UTC()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := u.Admit(context.Background(), key, at)
			if err != nil {
				t.Error(err)
				return
			}
			u.Record(key.ID, 1, 2, 3, at, b)
		}()
	}
	wg.Wait()
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close(context.Background())
	for _, s := range statusTest(t, reloaded, key, at) {
		if !s.StartsAt.Equal(at) || s.Requests != 20 || s.ChargedMicrocredits != 60 {
			t.Fatalf("reload: %+v", s)
		}
	}
}

func TestIndependentWindowsIgnoreLegacyBucketsAndPersistBeforeUsage(t *testing.T) {
	u, path := independentManager(t)
	key := windowTestKey("legacy")
	at := time.Now().UTC()
	start, _, _ := rateLimitWindowBounds("5h", at, time.UTC)
	_, err := u.db.Exec(`INSERT INTO usage_buckets(api_key_id,window,window_start,requests,updated_at) VALUES(?, '5h', ?, 99, ?)`, key.ID, formatTime(start), formatTime(at))
	if err != nil {
		t.Fatal(err)
	}
	b := admitTest(t, u, key, start) // exact collision with an old aligned bucket
	// No Record or Flush: window anchor must already survive reopening.
	restored, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(context.Background())
	s := statusTest(t, restored, key, start)
	if !s[0].StartsAt.Equal(b["5h"]) || s[0].Requests != 0 {
		t.Fatalf("legacy/reset: %+v", s)
	}
}

func TestIndependentWindowWriteFailureDoesNotStartWindow(t *testing.T) {
	u, _ := independentManager(t)
	key := windowTestKey("failure")
	if _, err := u.db.Exec(`CREATE TRIGGER fail_window BEFORE INSERT ON usage_windows BEGIN SELECT RAISE(ABORT, 'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Admit(t.Context(), key, time.Now()); err == nil {
		t.Fatal("admitted without persistent anchor")
	}
	if len(u.windows) != 0 {
		t.Fatal("failed transaction changed memory")
	}
}
