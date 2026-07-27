package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const splitMigrationName = "split-sqlite-v1"
const splitBackupCompleteMarker = ".transit-hub-backup-complete"

type SplitMigrationOptions struct {
	ControlPath   string
	UsagePath     string
	TelemetryPath string
	Retention     time.Duration
	Location      *time.Location
	Now           time.Time
	BackupDir     string
}

type SplitMigrationReport struct {
	Migration           string `json:"migration"`
	AlreadyApplied      bool   `json:"already_applied"`
	UsageTotals         int64  `json:"usage_totals"`
	UsageBuckets        int64  `json:"usage_buckets"`
	RequestLogs         int64  `json:"request_logs"`
	APISessions         int64  `json:"api_sessions"`
	ControlQuickCheck   string `json:"control_quick_check"`
	UsageQuickCheck     string `json:"usage_quick_check"`
	TelemetryQuickCheck string `json:"telemetry_quick_check"`
	BackupPath          string `json:"backup_path,omitempty"`
}

func SplitDatabases(ctx context.Context, options SplitMigrationOptions) (SplitMigrationReport, error) {
	options = normalizeSplitOptions(options)
	alreadyApplied, err := splitAlreadyApplied(ctx, options)
	if err != nil {
		return SplitMigrationReport{}, err
	}
	if alreadyApplied {
		report, err := VerifySplitDatabases(ctx, options)
		report.AlreadyApplied = true
		return report, err
	}
	for _, path := range []string{options.ControlPath, options.UsagePath, options.TelemetryPath} {
		if err := checkpointExistingDatabase(ctx, path); err != nil {
			return SplitMigrationReport{}, fmt.Errorf("checkpoint %s before backup: %w", path, err)
		}
	}
	backupPath, err := backupSplitData(options)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("backup data directory: %w", err)
	}

	control, usage, telemetry, err := openMigrationDatabases(options)
	if err != nil {
		return SplitMigrationReport{}, err
	}
	defer control.Close()
	defer usage.Close()
	defer telemetry.Close()

	usageTotals, err := copyLegacyUsageTotals(ctx, control, usage)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("copy usage totals: %w", err)
	}
	usageBuckets, err := aggregateLegacyUsageBuckets(ctx, control, usage, options)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("aggregate usage buckets: %w", err)
	}
	requestLogs, err := copyLegacyRequestLogs(ctx, control, telemetry, options)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("copy request logs: %w", err)
	}
	apiSessions, err := copyLegacyAPISessions(ctx, control, telemetry, options)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("copy api sessions: %w", err)
	}
	if err := markMigration(ctx, usage, splitMigrationName); err != nil {
		return SplitMigrationReport{}, err
	}
	if err := markMigration(ctx, telemetry, splitMigrationName); err != nil {
		return SplitMigrationReport{}, err
	}

	report, err := verifyOpenDatabases(ctx, control, usage, telemetry, options)
	report.UsageTotals = usageTotals
	report.UsageBuckets = usageBuckets
	report.RequestLogs = requestLogs
	report.APISessions = apiSessions
	report.BackupPath = backupPath
	return report, err
}

func splitAlreadyApplied(ctx context.Context, options SplitMigrationOptions) (bool, error) {
	for _, path := range []string{options.UsagePath, options.TelemetryPath} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		db, err := openSQLite(path, 5*time.Second)
		if err != nil {
			return false, err
		}
		exists, err := tableExists(ctx, db, "schema_migrations")
		if err == nil && exists {
			exists, err = migrationApplied(ctx, db, splitMigrationName)
		}
		closeErr := db.Close()
		if err != nil {
			return false, err
		}
		if closeErr != nil {
			return false, closeErr
		}
		if !exists {
			return false, nil
		}
	}
	return true, nil
}

func checkpointExistingDatabase(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	db, err := openSQLite(path, 5*time.Second)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

func VerifySplitDatabases(ctx context.Context, options SplitMigrationOptions) (SplitMigrationReport, error) {
	options = normalizeSplitOptions(options)
	control, usage, telemetry, err := openMigrationDatabases(options)
	if err != nil {
		return SplitMigrationReport{}, err
	}
	defer control.Close()
	defer usage.Close()
	defer telemetry.Close()
	return verifyOpenDatabases(ctx, control, usage, telemetry, options)
}

func MergeBackDatabases(ctx context.Context, options SplitMigrationOptions) (SplitMigrationReport, error) {
	options = normalizeSplitOptions(options)
	control, usage, telemetry, err := openMigrationDatabases(options)
	if err != nil {
		return SplitMigrationReport{}, err
	}
	defer control.Close()
	defer usage.Close()
	defer telemetry.Close()
	if err := ensureLegacyTelemetrySchema(control); err != nil {
		return SplitMigrationReport{}, err
	}

	logs, err := mergeTelemetryLogsBack(ctx, telemetry, control)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("merge request logs back: %w", err)
	}
	sessions, err := mergeTelemetrySessionsBack(ctx, telemetry, control)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("merge api sessions back: %w", err)
	}
	totals, err := mergeUsageTotalsBack(ctx, usage, control)
	if err != nil {
		return SplitMigrationReport{}, fmt.Errorf("merge usage totals back: %w", err)
	}
	_, _ = control.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	report, verifyErr := verifyOpenDatabases(ctx, control, usage, telemetry, options)
	report.UsageTotals = totals
	report.RequestLogs = logs
	report.APISessions = sessions
	return report, verifyErr
}

func normalizeSplitOptions(options SplitMigrationOptions) SplitMigrationOptions {
	if options.ControlPath == "" {
		options.ControlPath = "data/transit-hub.db"
	}
	if options.UsagePath == "" {
		options.UsagePath = "data/transit-hub-usage.db"
	}
	if options.TelemetryPath == "" {
		options.TelemetryPath = "data/transit-hub-telemetry.db"
	}
	if options.Retention <= 0 {
		options.Retention = 30 * 24 * time.Hour
	}
	if options.Location == nil {
		options.Location = time.UTC
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	} else {
		options.Now = options.Now.UTC()
	}
	if options.BackupDir == "" {
		controlDir := filepath.Clean(filepath.Dir(options.ControlPath))
		options.BackupDir = filepath.Join(controlDir, "backups", "split-"+options.Now.Format("20060102T150405Z"))
	}
	return options
}

func backupSplitData(options SplitMigrationOptions) (string, error) {
	source := filepath.Clean(filepath.Dir(options.ControlPath))
	destination := filepath.Clean(options.BackupDir)
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if sourceAbs == destinationAbs {
		return "", errors.New("backup directory must differ from control data directory")
	}
	relative, err := filepath.Rel(sourceAbs, destinationAbs)
	if err != nil {
		return "", err
	}
	if relative == "." {
		return "", errors.New("backup directory must differ from control data directory")
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) < 2 || parts[0] != "backups" {
			return "", errors.New("backup directory inside the control data directory must be under backups/")
		}
	}
	if _, err := os.Stat(destinationAbs); err == nil {
		if _, markerErr := os.Stat(filepath.Join(destinationAbs, splitBackupCompleteMarker)); markerErr == nil {
			return destinationAbs, nil
		} else if !errors.Is(markerErr, os.ErrNotExist) {
			return "", markerErr
		}
		return "", fmt.Errorf("incomplete backup directory already exists: %s", destinationAbs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := copyDirectory(sourceAbs, destinationAbs, destinationAbs); err != nil {
		return "", err
	}
	if err := os.WriteFile(
		filepath.Join(destinationAbs, splitBackupCompleteMarker),
		[]byte(formatTime(time.Now().UTC())+"\n"),
		0o600,
	); err != nil {
		return "", err
	}
	return destinationAbs, nil
}

func copyDirectory(source, destination, excludedDestination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		destinationPath := filepath.Join(destination, entry.Name())
		if sourcePath == excludedDestination || strings.HasPrefix(excludedDestination, sourcePath+string(filepath.Separator)) {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entryInfo.IsDir():
			if err := copyDirectory(sourcePath, destinationPath, excludedDestination); err != nil {
				return err
			}
		case entryInfo.Mode().IsRegular():
			if err := copyRegularFile(sourcePath, destinationPath, entryInfo.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func openMigrationDatabases(options SplitMigrationOptions) (*sql.DB, *sql.DB, *sql.DB, error) {
	control, err := openSQLite(options.ControlPath, 5*time.Second)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open control database: %w", err)
	}
	usage, err := openSQLite(options.UsagePath, 5*time.Second)
	if err != nil {
		_ = control.Close()
		return nil, nil, nil, fmt.Errorf("open usage database: %w", err)
	}
	if err := migrateUsage(usage); err != nil {
		_ = control.Close()
		_ = usage.Close()
		return nil, nil, nil, err
	}
	telemetry, err := openSQLite(options.TelemetryPath, 5*time.Second)
	if err != nil {
		_ = control.Close()
		_ = usage.Close()
		return nil, nil, nil, fmt.Errorf("open telemetry database: %w", err)
	}
	if err := migrateTelemetry(telemetry); err != nil {
		_ = control.Close()
		_ = usage.Close()
		_ = telemetry.Close()
		return nil, nil, nil, err
	}
	return control, usage, telemetry, nil
}

func copyLegacyUsageTotals(ctx context.Context, control, usage *sql.DB) (int64, error) {
	rows, err := control.QueryContext(ctx, `
		SELECT id, used_requests, used_tokens, last_used_at, updated_at FROM api_keys
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := usage.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		var id, updatedAt string
		var requests, tokens int64
		var lastUsedAt sql.NullString
		if err := rows.Scan(&id, &requests, &tokens, &lastUsedAt, &updatedAt); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_totals (api_key_id, used_requests, used_tokens, last_used_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id) DO UPDATE SET
				used_requests = excluded.used_requests,
				used_tokens = excluded.used_tokens,
				last_used_at = excluded.last_used_at,
				updated_at = excluded.updated_at
		`, id, requests, tokens, nullableString(lastUsedAt), updatedAt); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func aggregateLegacyUsageBuckets(ctx context.Context, control, usage *sql.DB, options SplitMigrationOptions) (int64, error) {
	buckets, err := loadLegacyUsageBuckets(ctx, control, options)
	if err != nil {
		return 0, err
	}
	tx, err := usage.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_buckets`); err != nil {
		return 0, err
	}
	for key, value := range buckets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_buckets (
				api_key_id, window, window_start, requests, tokens, cost_micro, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, key.APIKeyID, key.Window, key.WindowStart, value.Requests, value.Tokens,
			value.CostMicro, formatTime(value.UpdatedAt)); err != nil {
			return 0, err
		}
	}
	return int64(len(buckets)), tx.Commit()
}

func loadLegacyUsageBuckets(ctx context.Context, control *sql.DB, options SplitMigrationOptions) (map[usageBucketKey]usageBucketValue, error) {
	exists, err := tableExists(ctx, control, "request_logs")
	if err != nil || !exists {
		return map[usageBucketKey]usageBucketValue{}, err
	}
	cutoff := formatTime(options.Now.Add(-options.Retention))
	rows, err := control.QueryContext(ctx, `
		SELECT api_key_id, request_tokens, response_tokens, cost_micro, created_at
		FROM request_logs
		WHERE created_at >= ?
		ORDER BY created_at ASC
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	currentStarts := map[string]string{}
	for _, window := range supportedRateLimitWindows {
		start, _, err := rateLimitWindowBounds(window, options.Now, options.Location)
		if err != nil {
			return nil, err
		}
		currentStarts[window] = formatTime(start)
	}
	buckets := map[usageBucketKey]usageBucketValue{}
	for rows.Next() {
		var apiKeyID, createdAt string
		var requestTokens, responseTokens, costMicro int64
		if err := rows.Scan(&apiKeyID, &requestTokens, &responseTokens, &costMicro, &createdAt); err != nil {
			return nil, err
		}
		parsed, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		for _, window := range supportedRateLimitWindows {
			start, _, err := rateLimitWindowBounds(window, parsed, options.Location)
			if err != nil {
				return nil, err
			}
			startText := formatTime(start)
			if startText != currentStarts[window] {
				continue
			}
			key := usageBucketKey{APIKeyID: apiKeyID, Window: window, WindowStart: startText}
			value := buckets[key]
			value.Requests++
			value.Tokens += requestTokens + responseTokens
			value.CostMicro += costMicro
			value.UpdatedAt = options.Now
			buckets[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buckets, nil
}

func copyLegacyRequestLogs(ctx context.Context, control, telemetry *sql.DB, options SplitMigrationOptions) (int64, error) {
	exists, err := tableExists(ctx, control, "request_logs")
	if err != nil || !exists {
		return 0, err
	}
	rows, err := control.QueryContext(ctx, `
		SELECT l.id, l.api_key_id, COALESCE(k.name, ''), COALESCE(k.key_prefix, ''),
		       l.protocol, l.public_model, l.upstream_model, l.provider, l.pool, l.account,
		       l.device_id, l.source, l.status_code, l.latency_ms, l.request_tokens,
		       l.response_tokens, l.cache_hit_tokens, l.cache_miss_tokens, l.cost_micro,
		       l.estimated, l.error_type, l.created_at
		FROM request_logs l
		LEFT JOIN api_keys k ON k.id = l.api_key_id
		WHERE l.created_at >= ?
		ORDER BY l.id ASC
	`, formatTime(options.Now.Add(-options.Retention)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := telemetry.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		values := make([]any, 22)
		targets := make([]any, len(values))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO request_logs (
				id, api_key_id, api_key_name, key_prefix, protocol, public_model,
				upstream_model, provider, pool, account, device_id, source, status_code,
				latency_ms, request_tokens, response_tokens, cache_hit_tokens,
				cache_miss_tokens, cost_micro, estimated, error_type, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				api_key_id = excluded.api_key_id,
				api_key_name = excluded.api_key_name,
				key_prefix = excluded.key_prefix,
				protocol = excluded.protocol,
				public_model = excluded.public_model,
				upstream_model = excluded.upstream_model,
				provider = excluded.provider,
				pool = excluded.pool,
				account = excluded.account,
				device_id = excluded.device_id,
				source = excluded.source,
				status_code = excluded.status_code,
				latency_ms = excluded.latency_ms,
				request_tokens = excluded.request_tokens,
				response_tokens = excluded.response_tokens,
				cache_hit_tokens = excluded.cache_hit_tokens,
				cache_miss_tokens = excluded.cache_miss_tokens,
				cost_micro = excluded.cost_micro,
				estimated = excluded.estimated,
				error_type = excluded.error_type,
				created_at = excluded.created_at
		`, values...); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func copyLegacyAPISessions(ctx context.Context, control, telemetry *sql.DB, options SplitMigrationOptions) (int64, error) {
	exists, err := tableExists(ctx, control, "api_key_sessions")
	if err != nil || !exists {
		return 0, err
	}
	rows, err := control.QueryContext(ctx, `
		SELECT s.api_key_id, COALESCE(k.name, ''), COALESCE(k.key_prefix, ''),
		       s.device_id, s.source, s.first_seen_at, s.last_seen_at, s.last_status_code,
		       s.request_count, s.token_count
		FROM api_key_sessions s
		LEFT JOIN api_keys k ON k.id = s.api_key_id
		WHERE s.last_seen_at >= ?
	`, formatTime(options.Now.Add(-options.Retention)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := telemetry.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		values := make([]any, 10)
		targets := make([]any, len(values))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_key_sessions (
				api_key_id, api_key_name, key_prefix, device_id, source, first_seen_at,
				last_seen_at, last_status_code, request_count, token_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id, device_id, source) DO UPDATE SET
				api_key_name = excluded.api_key_name,
				key_prefix = excluded.key_prefix,
				first_seen_at = excluded.first_seen_at,
				last_seen_at = excluded.last_seen_at,
				last_status_code = excluded.last_status_code,
				request_count = excluded.request_count,
				token_count = excluded.token_count
		`, values...); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func verifyOpenDatabases(ctx context.Context, control, usage, telemetry *sql.DB, options SplitMigrationOptions) (SplitMigrationReport, error) {
	report := SplitMigrationReport{Migration: splitMigrationName}
	var err error
	if report.ControlQuickCheck, err = quickCheck(ctx, control); err != nil {
		return report, err
	}
	if report.UsageQuickCheck, err = quickCheck(ctx, usage); err != nil {
		return report, err
	}
	if report.TelemetryQuickCheck, err = quickCheck(ctx, telemetry); err != nil {
		return report, err
	}
	if err := usage.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_totals`).Scan(&report.UsageTotals); err != nil {
		return report, err
	}
	if err := usage.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_buckets`).Scan(&report.UsageBuckets); err != nil {
		return report, err
	}
	if err := telemetry.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&report.RequestLogs); err != nil {
		return report, err
	}
	if err := telemetry.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_key_sessions`).Scan(&report.APISessions); err != nil {
		return report, err
	}
	var controlKeys int64
	if err := control.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys`).Scan(&controlKeys); err != nil {
		return report, err
	}
	if report.UsageTotals < controlKeys {
		return report, fmt.Errorf("usage totals count %d is less than control api key count %d", report.UsageTotals, controlKeys)
	}
	if legacyLogs, err := retainedLegacyRowCount(ctx, control, "request_logs", "created_at", options); err != nil {
		return report, err
	} else if report.RequestLogs < legacyLogs {
		return report, fmt.Errorf("telemetry request log count %d is less than retained control count %d", report.RequestLogs, legacyLogs)
	}
	if legacySessions, err := retainedLegacyRowCount(ctx, control, "api_key_sessions", "last_seen_at", options); err != nil {
		return report, err
	} else if report.APISessions < legacySessions {
		return report, fmt.Errorf("telemetry session count %d is less than retained control count %d", report.APISessions, legacySessions)
	}
	controlTotals, err := loadLegacyTotalsForVerification(ctx, control)
	if err != nil {
		return report, err
	}
	for apiKeyID, controlTotal := range controlTotals {
		var usageRequests, usageTokens int64
		if err := usage.QueryRowContext(ctx, `
			SELECT used_requests, used_tokens FROM usage_totals WHERE api_key_id = ?
		`, apiKeyID).Scan(&usageRequests, &usageTokens); err != nil {
			return report, fmt.Errorf("verify usage total %s: %w", apiKeyID, err)
		}
		if usageRequests < controlTotal.UsedRequests || usageTokens < controlTotal.UsedTokens {
			return report, fmt.Errorf(
				"usage total %s is behind control: usage=(%d,%d) control=(%d,%d)",
				apiKeyID, usageRequests, usageTokens, controlTotal.UsedRequests, controlTotal.UsedTokens,
			)
		}
	}
	legacyBuckets, err := loadLegacyUsageBuckets(ctx, control, options)
	if err != nil {
		return report, err
	}
	for key, expected := range legacyBuckets {
		var requests, tokens, costMicro int64
		if err := usage.QueryRowContext(ctx, `
			SELECT requests, tokens, cost_micro
			FROM usage_buckets
			WHERE api_key_id = ? AND window = ? AND window_start = ?
		`, key.APIKeyID, key.Window, key.WindowStart).Scan(&requests, &tokens, &costMicro); err != nil {
			return report, fmt.Errorf("verify usage bucket %s/%s/%s: %w", key.APIKeyID, key.Window, key.WindowStart, err)
		}
		if requests < expected.Requests || tokens < expected.Tokens || costMicro < expected.CostMicro {
			return report, fmt.Errorf(
				"usage bucket %s/%s/%s is behind control: usage=(%d,%d,%d) control=(%d,%d,%d)",
				key.APIKeyID, key.Window, key.WindowStart,
				requests, tokens, costMicro, expected.Requests, expected.Tokens, expected.CostMicro,
			)
		}
	}
	return report, nil
}

func retainedLegacyRowCount(ctx context.Context, control *sql.DB, table, column string, options SplitMigrationOptions) (int64, error) {
	exists, err := tableExists(ctx, control, table)
	if err != nil || !exists {
		return 0, err
	}
	var count int64
	err = control.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE %s >= ?`, table, column,
	), formatTime(options.Now.Add(-options.Retention))).Scan(&count)
	return count, err
}

func loadLegacyTotalsForVerification(ctx context.Context, control *sql.DB) (map[string]UsageTotal, error) {
	rows, err := control.QueryContext(ctx, `SELECT id, used_requests, used_tokens FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]UsageTotal{}
	for rows.Next() {
		var apiKeyID string
		var total UsageTotal
		if err := rows.Scan(&apiKeyID, &total.UsedRequests, &total.UsedTokens); err != nil {
			return nil, err
		}
		totals[apiKeyID] = total
	}
	return totals, rows.Err()
}

func mergeTelemetryLogsBack(ctx context.Context, telemetry, control *sql.DB) (int64, error) {
	rows, err := telemetry.QueryContext(ctx, `
		SELECT id, api_key_id, protocol, public_model, upstream_model, provider, pool,
		       account, device_id, source, status_code, latency_ms, request_tokens,
		       response_tokens, cache_hit_tokens, cache_miss_tokens, cost_micro,
		       estimated, error_type, created_at
		FROM request_logs ORDER BY id ASC
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := control.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		values := make([]any, 20)
		targets := make([]any, len(values))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO request_logs (
				id, api_key_id, protocol, public_model, upstream_model, provider, pool,
				account, device_id, source, status_code, latency_ms, request_tokens,
				response_tokens, cache_hit_tokens, cache_miss_tokens, cost_micro,
				estimated, error_type, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, values...)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		count += affected
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func mergeTelemetrySessionsBack(ctx context.Context, telemetry, control *sql.DB) (int64, error) {
	rows, err := telemetry.QueryContext(ctx, `
		SELECT api_key_id, device_id, source, first_seen_at, last_seen_at,
		       last_status_code, request_count, token_count
		FROM api_key_sessions
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := control.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		values := make([]any, 8)
		targets := make([]any, len(values))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_key_sessions (
				api_key_id, device_id, source, first_seen_at, last_seen_at,
				last_status_code, request_count, token_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id, device_id, source) DO UPDATE SET
				first_seen_at = MIN(api_key_sessions.first_seen_at, excluded.first_seen_at),
				last_seen_at = MAX(api_key_sessions.last_seen_at, excluded.last_seen_at),
				last_status_code = excluded.last_status_code,
				request_count = excluded.request_count,
				token_count = excluded.token_count
		`, values...); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func mergeUsageTotalsBack(ctx context.Context, usage, control *sql.DB) (int64, error) {
	rows, err := usage.QueryContext(ctx, `
		SELECT api_key_id, used_requests, used_tokens, last_used_at, updated_at FROM usage_totals
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := control.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	for rows.Next() {
		var id, updatedAt string
		var requests, tokens int64
		var lastUsedAt sql.NullString
		if err := rows.Scan(&id, &requests, &tokens, &lastUsedAt, &updatedAt); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE api_keys
			SET used_requests = ?, used_tokens = ?, last_used_at = ?, updated_at = MAX(updated_at, ?)
			WHERE id = ?
		`, requests, tokens, nullableString(lastUsedAt), updatedAt, id)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		count += affected
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func ensureLegacyTelemetrySchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			api_key_id TEXT NOT NULL,
			protocol TEXT NOT NULL,
			public_model TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			provider TEXT NOT NULL,
			pool TEXT NOT NULL,
			account TEXT NOT NULL,
			device_id TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			request_tokens INTEGER NOT NULL,
			response_tokens INTEGER NOT NULL,
			cache_hit_tokens INTEGER NOT NULL DEFAULT 0,
			cache_miss_tokens INTEGER NOT NULL DEFAULT 0,
			cost_micro INTEGER NOT NULL DEFAULT 0,
			estimated INTEGER NOT NULL DEFAULT 0,
			error_type TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(api_key_id) REFERENCES api_keys(id)
		);
		CREATE TABLE IF NOT EXISTS api_key_sessions (
			api_key_id TEXT NOT NULL,
			device_id TEXT NOT NULL,
			source TEXT NOT NULL,
			first_seen_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			last_status_code INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			token_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(api_key_id, device_id, source),
			FOREIGN KEY(api_key_id) REFERENCES api_keys(id)
		);
	`)
	return err
}

func migrationApplied(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var value string
	err := db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func markMigration(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET applied_at = excluded.applied_at
	`, name, formatTime(time.Now().UTC()))
	return err
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?
	`, name).Scan(&count)
	return count > 0, err
}

func quickCheck(ctx context.Context, db *sql.DB) (string, error) {
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return "", err
	}
	if result != "ok" {
		return result, fmt.Errorf("sqlite quick_check: %s", result)
	}
	return result, nil
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func MarshalMigrationReport(report SplitMigrationReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}
