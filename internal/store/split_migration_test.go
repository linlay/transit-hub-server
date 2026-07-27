package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSplitVerifyAndMergeBackAreIdempotent(t *testing.T) {
	root := t.TempDir()
	options := SplitMigrationOptions{
		ControlPath:   filepath.Join(root, "control.db"),
		UsagePath:     filepath.Join(root, "usage.db"),
		TelemetryPath: filepath.Join(root, "telemetry.db"),
		Retention:     30 * 24 * time.Hour,
		Location:      time.UTC,
		Now:           time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC),
	}
	control := openTestStore(t, options.ControlPath)
	key, err := control.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "migration-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.db.ExecContext(t.Context(), `
		UPDATE api_keys SET used_requests = 8, used_tokens = 80, last_used_at = ? WHERE id = ?
	`, formatTime(options.Now.Add(-time.Minute)), key.ID); err != nil {
		t.Fatal(err)
	}
	if err := ensureLegacyTelemetrySchema(control.db); err != nil {
		t.Fatal(err)
	}
	insertLegacyMigrationLog(t, control, key.ID, options.Now.Add(-31*24*time.Hour), 1, 2)
	insertLegacyMigrationLog(t, control, key.ID, options.Now.Add(-10*time.Minute), 3, 4)
	if _, err := control.db.ExecContext(t.Context(), `
		INSERT INTO api_key_sessions (
			api_key_id, device_id, source, first_seen_at, last_seen_at,
			last_status_code, request_count, token_count
		) VALUES (?, 'device-1', 'codex', ?, ?, 200, 2, 7)
	`, key.ID, formatTime(options.Now.Add(-time.Hour)), formatTime(options.Now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	closeTestStore(t, control)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := SplitDatabases(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.AlreadyApplied || report.UsageTotals != 1 || report.UsageBuckets != 5 || report.RequestLogs != 1 || report.APISessions != 1 {
		t.Fatalf("unexpected split report: %#v", report)
	}
	if report.BackupPath == "" {
		t.Fatal("split report is missing backup path")
	}
	if _, err := os.Stat(filepath.Join(report.BackupPath, filepath.Base(options.ControlPath))); err != nil {
		t.Fatalf("control backup missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(report.BackupPath, splitBackupCompleteMarker)); err != nil {
		t.Fatalf("backup completion marker missing: %v", err)
	}
	again, err := SplitDatabases(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyApplied || again.RequestLogs != 1 {
		t.Fatalf("split was not idempotent: %#v", again)
	}
	verified, err := VerifySplitDatabases(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ControlQuickCheck != "ok" || verified.UsageQuickCheck != "ok" || verified.TelemetryQuickCheck != "ok" {
		t.Fatalf("quick_check failed: %#v", verified)
	}
	usageDB := openRawTestDB(t, options.UsagePath)
	if _, err := usageDB.ExecContext(t.Context(), `
		UPDATE usage_buckets SET requests = 0 WHERE api_key_id = ? AND window = '1h'
	`, key.ID); err != nil {
		t.Fatal(err)
	}
	closeRawTestDB(t, usageDB)
	if _, err := VerifySplitDatabases(ctx, options); err == nil {
		t.Fatal("verify unexpectedly accepted a usage bucket behind the control log aggregate")
	}
	usageDB = openRawTestDB(t, options.UsagePath)
	if _, err := usageDB.ExecContext(t.Context(), `
		UPDATE usage_buckets SET requests = 1 WHERE api_key_id = ? AND window = '1h'
	`, key.ID); err != nil {
		t.Fatal(err)
	}
	closeRawTestDB(t, usageDB)

	usage, err := NewUsageManager(options.UsagePath, options.Location)
	if err != nil {
		t.Fatal(err)
	}
	usage.Record(key.ID, 5, 6, 9, options.Now.Add(time.Minute))
	if err := usage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	telemetry, err := NewTelemetry(options.TelemetryPath, options.Retention)
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(testTelemetryLog(key.ID, options.Now.Add(time.Minute)))
	if err := telemetry.Close(ctx); err != nil {
		t.Fatal(err)
	}

	merged, err := MergeBackDatabases(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if merged.RequestLogs != 1 {
		t.Fatalf("merge should add exactly one post-split log: %#v", merged)
	}
	mergedAgain, err := MergeBackDatabases(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if mergedAgain.RequestLogs != 0 {
		t.Fatalf("merge-back was not idempotent: %#v", mergedAgain)
	}

	control = openTestStore(t, options.ControlPath)
	defer closeTestStore(t, control)
	migratedKey, err := control.GetAPIKey(t.Context(), key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migratedKey.UsedRequests != 9 || migratedKey.UsedTokens != 91 {
		t.Fatalf("usage totals were not merged back: %#v", migratedKey)
	}
	var logCount int64
	if err := control.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM request_logs`).Scan(&logCount); err != nil {
		t.Fatal(err)
	}
	if logCount != 3 {
		t.Fatalf("control request log count=%d want=3", logCount)
	}
}

func insertLegacyMigrationLog(t *testing.T, control *Store, apiKeyID string, createdAt time.Time, requestTokens, responseTokens int64) {
	t.Helper()
	if _, err := control.db.ExecContext(t.Context(), `
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			status_code, latency_ms, request_tokens, response_tokens, cost_micro, created_at
		) VALUES (?, 'openai', 'model', 'upstream', 'provider', 'default', 'account',
			200, 10, ?, ?, 12, ?)
	`, apiKeyID, requestTokens, responseTokens, formatTime(createdAt)); err != nil {
		t.Fatal(err)
	}
}
