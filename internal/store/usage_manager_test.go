package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUsageManagerConcurrentRecordFlushAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	manager, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	const workers = 12
	const eventsPerWorker = 200
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for event := 0; event < eventsPerWorker; event++ {
				manager.Record("key-1", 2, 3, 7, at)
			}
		}()
	}
	wait.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := reloaded.Close(closeCtx); err != nil {
			t.Fatal(err)
		}
	})
	total := reloaded.Total("key-1")
	wantRequests := int64(workers * eventsPerWorker)
	if total.UsedRequests != wantRequests || total.UsedTokens != wantRequests*5 {
		t.Fatalf("unexpected persisted total: %#v", total)
	}
	statuses, err := reloaded.RateLimitStatuses("key-1", []RateLimit{{
		Window: RateLimitWindow1H,
	}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Requests != wantRequests || statuses[0].CostMicro != wantRequests*7 {
		t.Fatalf("unexpected persisted bucket: %#v", statuses)
	}
}

func TestUsageManagerRetainsDirtyDeltaWhileDatabaseLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	manager, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})

	locker := openRawTestDB(t, path)
	defer closeRawTestDB(t, locker)
	execTestSQL(t, locker, `PRAGMA busy_timeout = 0; BEGIN IMMEDIATE`)
	manager.Record("locked-key", 4, 6, 9, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = manager.Flush(flushCtx)
	cancel()
	if err == nil {
		t.Fatal("flush unexpectedly succeeded while usage database was write-locked")
	}
	if manager.PendingUpdates() != 1 || !manager.Degraded() {
		t.Fatalf("dirty delta was not retained: pending=%d degraded=%v", manager.PendingUpdates(), manager.Degraded())
	}
	if total := manager.Total("locked-key"); total.UsedRequests != 1 || total.UsedTokens != 10 {
		t.Fatalf("in-memory usage changed after failed flush: %#v", total)
	}

	execTestSQL(t, locker, `ROLLBACK`)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := manager.Flush(retryCtx); err != nil {
		t.Fatalf("flush after unlock: %v", err)
	}
	if manager.PendingUpdates() != 0 || manager.Degraded() {
		t.Fatalf("usage did not recover: pending=%d degraded=%v", manager.PendingUpdates(), manager.Degraded())
	}
}

func TestUsageManagerUpdatesAllFiveWindows(t *testing.T) {
	manager, err := NewUsageManager(filepath.Join(t.TempDir(), "usage.db"), time.FixedZone("UTC+8", 8*60*60))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
	at := time.Date(2026, 7, 27, 23, 59, 59, 0, time.FixedZone("UTC+8", 8*60*60))
	manager.Record("key-all-windows", 1, 2, 3, at)
	limits := make([]RateLimit, 0, len(supportedRateLimitWindows))
	for _, window := range supportedRateLimitWindows {
		limits = append(limits, RateLimit{Window: window})
	}
	statuses, err := manager.RateLimitStatuses("key-all-windows", limits, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != len(supportedRateLimitWindows) {
		t.Fatalf("statuses=%d want=%d", len(statuses), len(supportedRateLimitWindows))
	}
	for _, status := range statuses {
		if status.Requests != 1 || status.Tokens != 3 || status.CostMicro != 3 {
			t.Fatalf("window %s missing usage: %#v", status.Window, status)
		}
	}
}
