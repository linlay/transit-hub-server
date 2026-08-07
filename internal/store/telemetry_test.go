package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCanceledTelemetryQueryDoesNotDegradeService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	telemetry, err := NewTelemetry(path, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})

	connection, err := telemetry.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := telemetry.ProviderUsage(ctx, ProviderUsageQuery{})
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for telemetry.db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if telemetry.db.Stats().WaitCount == 0 {
		cancel()
		t.Fatal("provider usage query did not wait for the occupied database connection")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("provider usage error = %v, want context canceled", err)
	}
	if telemetry.Degraded() {
		t.Fatal("client cancellation degraded telemetry service")
	}
}

func TestTelemetryQueueDoesNotBlockOnWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	telemetry, err := NewTelemetry(path, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
	locker := openRawTestDB(t, path)
	defer closeRawTestDB(t, locker)
	execTestSQL(t, locker, `PRAGMA busy_timeout = 0; BEGIN IMMEDIATE`)

	started := time.Now()
	if !telemetry.Enqueue(testTelemetryLog("locked-key", time.Now().UTC())) {
		t.Fatal("enqueue unexpectedly failed")
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("enqueue waited for SQLite lock: %s", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = telemetry.Flush(ctx)
	cancel()
	if err == nil {
		t.Fatal("flush unexpectedly succeeded while telemetry database was write-locked")
	}
	if telemetry.DroppedLogs() != 1 || !telemetry.Degraded() {
		t.Fatalf("failed batch was not dropped: dropped=%d degraded=%v", telemetry.DroppedLogs(), telemetry.Degraded())
	}
	execTestSQL(t, locker, `ROLLBACK`)
}

func TestTelemetryPruneDeletesOnlyExpiredRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	telemetry, err := NewTelemetry(path, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	telemetry.Enqueue(testTelemetryLog("old", now.Add(-31*24*time.Hour)))
	telemetry.Enqueue(testTelemetryLog("current", now.Add(-29*24*time.Hour)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := telemetry.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	result, err := telemetry.ListRequestLogs(ctx, RequestLogQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].APIKeyID != "current" {
		t.Fatalf("unexpected logs after prune: %#v", result)
	}
}

func testTelemetryLog(apiKeyID string, at time.Time) RequestLog {
	return RequestLog{
		APIKeyID:       apiKeyID,
		APIKeyName:     apiKeyID,
		KeyPrefix:      "th_test",
		Protocol:       "openai",
		PublicModel:    "model",
		UpstreamModel:  "upstream",
		Provider:       "provider",
		Pool:           "default",
		Account:        "account",
		StatusCode:     200,
		Latency:        time.Millisecond,
		RequestTokens:  1,
		ResponseTokens: 2,
		CreatedAt:      at,
	}
}
