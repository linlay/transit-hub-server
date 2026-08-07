package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTelemetryQueueSize     = 10_000
	defaultTelemetryBatchSize     = 100
	defaultTelemetryFlushInterval = 250 * time.Millisecond
	defaultTelemetryWriteTimeout  = time.Second
)

var ErrTelemetryUnavailable = errors.New("telemetry unavailable")

// Telemetry owns the best-effort request log and API-session database. Enqueue
// never blocks and writer failures are intentionally isolated from proxy and
// control-database operations.
type Telemetry struct {
	path      string
	retention time.Duration

	connectMu sync.Mutex
	dbMu      sync.RWMutex
	db        *sql.DB

	queue         chan RequestLog
	flushRequests chan chan error
	dropped       atomic.Int64
	degraded      atomic.Bool

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewTelemetry(path string, retention time.Duration) (*Telemetry, error) {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	telemetry := &Telemetry{
		path:          path,
		retention:     retention,
		queue:         make(chan RequestLog, defaultTelemetryQueueSize),
		flushRequests: make(chan chan error),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	err := telemetry.connect()
	if err != nil {
		telemetry.degraded.Store(true)
	}
	go telemetry.run()
	return telemetry, err
}

func (t *Telemetry) Enqueue(entry RequestLog) bool {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	} else {
		entry.CreatedAt = entry.CreatedAt.UTC()
	}
	select {
	case t.queue <- entry:
		return true
	default:
		t.dropped.Add(1)
		t.degraded.Store(true)
		return false
	}
}

func (t *Telemetry) DroppedLogs() int64 {
	return t.dropped.Load()
}

func (t *Telemetry) Flush(ctx context.Context) error {
	result := make(chan error, 1)
	select {
	case t.flushRequests <- result:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Telemetry) Degraded() bool {
	return t.degraded.Load()
}

func (t *Telemetry) markQueryFailure(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	t.degraded.Store(true)
}

func (t *Telemetry) Close(ctx context.Context) error {
	t.stopOnce.Do(func() { close(t.stop) })
	select {
	case <-t.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	t.dbMu.Lock()
	defer t.dbMu.Unlock()
	if t.db != nil {
		err := t.db.Close()
		t.db = nil
		return err
	}
	return nil
}

func (t *Telemetry) run() {
	defer close(t.done)
	flushTicker := time.NewTicker(defaultTelemetryFlushInterval)
	retentionTicker := time.NewTicker(24 * time.Hour)
	defer flushTicker.Stop()
	defer retentionTicker.Stop()

	batch := make([]RequestLog, 0, defaultTelemetryBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultTelemetryWriteTimeout)
		err := t.writeBatch(ctx, batch)
		cancel()
		if err != nil {
			t.dropped.Add(int64(len(batch)))
			t.degraded.Store(true)
		} else {
			t.degraded.Store(false)
		}
		batch = batch[:0]
		return err
	}
	drain := func() {
		for len(batch) < defaultTelemetryBatchSize {
			select {
			case entry := <-t.queue:
				batch = append(batch, entry)
			default:
				return
			}
		}
	}

	for {
		select {
		case entry := <-t.queue:
			batch = append(batch, entry)
			if len(batch) >= defaultTelemetryBatchSize {
				_ = flush()
			}
		case <-flushTicker.C:
			_ = flush()
		case result := <-t.flushRequests:
			var flushErr error
			for {
				drain()
				if err := flush(); err != nil {
					flushErr = err
				}
				if len(t.queue) == 0 {
					break
				}
			}
			result <- flushErr
		case <-retentionTicker.C:
			_ = flush()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := t.Prune(ctx, time.Now().UTC()); err != nil {
				t.degraded.Store(true)
			}
			cancel()
		case <-t.stop:
			for {
				select {
				case entry := <-t.queue:
					batch = append(batch, entry)
					if len(batch) >= defaultTelemetryBatchSize {
						_ = flush()
					}
				default:
					_ = flush()
					return
				}
			}
		}
	}
}

func (t *Telemetry) writeBatch(ctx context.Context, batch []RequestLog) error {
	if err := t.ensureConnected(); err != nil {
		return err
	}
	db, err := t.currentDB()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, entry := range batch {
		deviceID := sanitizeSessionValue(entry.DeviceID)
		source := ""
		if deviceID != "" {
			source = sanitizeSessionValue(defaultSource(entry.Source))
		}
		createdAt := entry.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO request_logs (
				api_key_id, api_key_name, key_prefix, protocol, public_model, upstream_model,
				provider, pool, account, device_id, source, status_code, latency_ms,
				request_tokens, response_tokens, cache_hit_tokens, cache_miss_tokens,
				cost_micro, estimated, error_type, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, entry.APIKeyID, entry.APIKeyName, entry.KeyPrefix, entry.Protocol, entry.PublicModel,
			entry.UpstreamModel, entry.Provider, entry.Pool, entry.Account, deviceID, source,
			entry.StatusCode, entry.Latency.Milliseconds(), entry.RequestTokens, entry.ResponseTokens,
			entry.CacheHitTokens, entry.CacheMissTokens, entry.CostMicro, boolInt(entry.Estimated),
			entry.ErrorType, formatTime(createdAt)); err != nil {
			return err
		}
		if deviceID == "" {
			continue
		}
		tokenDelta := entry.RequestTokens + entry.ResponseTokens
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_key_sessions (
				api_key_id, api_key_name, key_prefix, device_id, source, first_seen_at,
				last_seen_at, last_status_code, request_count, token_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(api_key_id, device_id, source) DO UPDATE SET
				api_key_name = excluded.api_key_name,
				key_prefix = excluded.key_prefix,
				last_seen_at = excluded.last_seen_at,
				last_status_code = excluded.last_status_code,
				request_count = api_key_sessions.request_count + 1,
				token_count = api_key_sessions.token_count + excluded.token_count
		`, entry.APIKeyID, entry.APIKeyName, entry.KeyPrefix, deviceID, source,
			formatTime(createdAt), formatTime(createdAt), entry.StatusCode, tokenDelta); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (t *Telemetry) connect() error {
	t.connectMu.Lock()
	defer t.connectMu.Unlock()

	t.dbMu.RLock()
	connected := t.db != nil
	t.dbMu.RUnlock()
	if connected {
		return nil
	}

	db, err := openSQLite(t.path, time.Second)
	if err != nil {
		return err
	}
	if err := migrateTelemetry(db); err != nil {
		_ = db.Close()
		return err
	}
	t.dbMu.Lock()
	defer t.dbMu.Unlock()
	if t.db != nil {
		_ = db.Close()
		return nil
	}
	t.db = db
	return nil
}

func (t *Telemetry) ensureConnected() error {
	t.dbMu.RLock()
	connected := t.db != nil
	t.dbMu.RUnlock()
	if connected {
		return nil
	}
	return t.connect()
}

func (t *Telemetry) currentDB() (*sql.DB, error) {
	t.dbMu.RLock()
	defer t.dbMu.RUnlock()
	if t.db == nil {
		return nil, ErrTelemetryUnavailable
	}
	return t.db, nil
}

func (t *Telemetry) Traffic(ctx context.Context, query TrafficQuery) ([]TrafficBucket, error) {
	if err := t.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := t.currentDB()
	if err != nil {
		return nil, err
	}
	bucketExpr := `substr(created_at, 1, 10)`
	switch strings.ToLower(strings.TrimSpace(query.Bucket)) {
	case "hour":
		bucketExpr = `substr(created_at, 1, 13)`
	case "month":
		bucketExpr = `substr(created_at, 1, 7)`
	}
	where, args := requestLogWhere(query.APIKeyID, query.From, query.To)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS bucket,
		       COUNT(*), COUNT(DISTINCT CASE
		           WHEN device_id <> '' THEN 'device:' || device_id
		           WHEN api_key_id <> '' THEN 'key:' || api_key_id
		       END),
		       COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(cache_hit_tokens), 0), COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucketExpr, where), args...)
	if err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	defer rows.Close()
	items := []TrafficBucket{}
	for rows.Next() {
		var item TrafficBucket
		if err := rows.Scan(&item.Bucket, &item.Requests, &item.UniqueDevices, &item.RequestTokens, &item.ResponseTokens,
			&item.CacheHitTokens, &item.CacheMissTokens, &item.CostMicro, &item.ErrorRequests,
			&item.AverageLatency); err != nil {
			return nil, err
		}
		fillTrafficDerived(&item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	modelRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS bucket, public_model, COUNT(*),
		       COALESCE(SUM(request_tokens + response_tokens), 0)
		FROM request_logs
		%s
		GROUP BY bucket, public_model
		ORDER BY bucket ASC, COUNT(*) DESC, public_model ASC
	`, bucketExpr, where), args...)
	if err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	defer modelRows.Close()
	bucketsByName := make(map[string]*TrafficBucket, len(items))
	for i := range items {
		bucketsByName[items[i].Bucket] = &items[i]
	}
	for modelRows.Next() {
		var bucket string
		var usage TrafficModelUsage
		if err := modelRows.Scan(&bucket, &usage.Model, &usage.Requests, &usage.TotalTokens); err != nil {
			return nil, err
		}
		if item := bucketsByName[bucket]; item != nil {
			item.Models = append(item.Models, usage)
		}
	}
	if err := modelRows.Err(); err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	return items, nil
}

func (t *Telemetry) RequestLogSummary(ctx context.Context, query RequestLogQuery) (TrafficBucket, error) {
	if err := t.Flush(ctx); err != nil {
		return TrafficBucket{}, err
	}
	db, err := t.currentDB()
	if err != nil {
		return TrafficBucket{}, err
	}
	where, args := requestLogWhere(query.APIKeyID, query.From, query.To)
	var summary TrafficBucket
	err = db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*), COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(cache_hit_tokens), 0), COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
	`, where), args...).Scan(&summary.Requests, &summary.RequestTokens, &summary.ResponseTokens,
		&summary.CacheHitTokens, &summary.CacheMissTokens, &summary.CostMicro,
		&summary.ErrorRequests, &summary.AverageLatency)
	if err != nil {
		t.markQueryFailure(err)
		return TrafficBucket{}, err
	}
	fillTrafficDerived(&summary)
	return summary, nil
}

func (t *Telemetry) ListRequestLogs(ctx context.Context, query RequestLogQuery) (RequestLogListResult, error) {
	if err := t.Flush(ctx); err != nil {
		return RequestLogListResult{}, err
	}
	db, err := t.currentDB()
	if err != nil {
		return RequestLogListResult{}, err
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	where, args := requestLogWhere(query.APIKeyID, query.From, query.To)
	var total int64
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM request_logs %s`, where), args...).Scan(&total); err != nil {
		t.markQueryFailure(err)
		return RequestLogListResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, api_key_id, api_key_name, protocol, public_model, upstream_model,
		       provider, pool, account, device_id, source, status_code, latency_ms,
		       request_tokens, response_tokens, cache_hit_tokens, cache_miss_tokens,
		       cost_micro, estimated, error_type, created_at
		FROM request_logs
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, where), queryArgs...)
	if err != nil {
		t.markQueryFailure(err)
		return RequestLogListResult{}, err
	}
	defer rows.Close()
	items := []RequestLogEntry{}
	for rows.Next() {
		var item RequestLogEntry
		var estimated int
		var createdAt string
		if err := rows.Scan(&item.ID, &item.APIKeyID, &item.APIKeyName, &item.Protocol,
			&item.PublicModel, &item.UpstreamModel, &item.Provider, &item.Pool, &item.Account,
			&item.DeviceID, &item.Source, &item.StatusCode, &item.LatencyMS, &item.RequestTokens,
			&item.ResponseTokens, &item.CacheHitTokens, &item.CacheMissTokens, &item.CostMicro,
			&estimated, &item.ErrorType, &createdAt); err != nil {
			return RequestLogListResult{}, err
		}
		item.Estimated = estimated != 0
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		item.CacheTotalTokens = item.CacheHitTokens + item.CacheMissTokens
		item.CacheHitRate = cacheHitRate(item.CacheHitTokens, item.CacheMissTokens)
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return RequestLogListResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.markQueryFailure(err)
		return RequestLogListResult{}, err
	}
	return RequestLogListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (t *Telemetry) ProviderUsage(ctx context.Context, query ProviderUsageQuery) ([]ProviderUsage, error) {
	if err := t.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := t.currentDB()
	if err != nil {
		return nil, err
	}
	where, args := requestLogWhere("", query.From, query.To)
	if where == "" {
		where = "WHERE provider <> ''"
	} else {
		where += " AND provider <> ''"
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider, COUNT(*), COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0), COALESCE(SUM(cache_hit_tokens), 0),
		       COALESCE(SUM(cache_miss_tokens), 0), COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
		GROUP BY provider
		ORDER BY COUNT(*) DESC, provider ASC
	`, where), args...)
	if err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	defer rows.Close()
	items := []ProviderUsage{}
	for rows.Next() {
		var item ProviderUsage
		if err := rows.Scan(&item.Provider, &item.Requests, &item.RequestTokens, &item.ResponseTokens,
			&item.CacheHitTokens, &item.CacheMissTokens, &item.CostMicro, &item.ErrorRequests,
			&item.AverageLatency); err != nil {
			return nil, err
		}
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		item.CacheTotalTokens = item.CacheHitTokens + item.CacheMissTokens
		item.CacheHitRate = cacheHitRate(item.CacheHitTokens, item.CacheMissTokens)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (t *Telemetry) ProviderAccountUsage(ctx context.Context, query ProviderUsageQuery) ([]ProviderAccountUsage, error) {
	if err := t.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := t.currentDB()
	if err != nil {
		return nil, err
	}
	where, args := requestLogWhere("", query.From, query.To)
	if where == "" {
		where = "WHERE provider <> '' AND account <> ''"
	} else {
		where += " AND provider <> '' AND account <> ''"
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider, pool, account, COUNT(*), COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0)
		FROM request_logs
		%s
		GROUP BY provider, pool, account
		ORDER BY provider ASC, pool ASC, COUNT(*) DESC, account ASC
	`, where), args...)
	if err != nil {
		t.markQueryFailure(err)
		return nil, err
	}
	defer rows.Close()
	items := []ProviderAccountUsage{}
	for rows.Next() {
		var item ProviderAccountUsage
		if err := rows.Scan(&item.Provider, &item.Pool, &item.Account, &item.Requests,
			&item.RequestTokens, &item.ResponseTokens, &item.ErrorRequests); err != nil {
			return nil, err
		}
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		items = append(items, item)
	}
	return items, rows.Err()
}

func (t *Telemetry) CountActiveSessions(ctx context.Context, activeWindow time.Duration) (int64, error) {
	if err := t.Flush(ctx); err != nil {
		return 0, err
	}
	db, err := t.currentDB()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-activeWindow)
	var count int64
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api_key_sessions WHERE last_seen_at >= ?
	`, formatTime(cutoff)).Scan(&count)
	if err != nil {
		t.markQueryFailure(err)
	}
	return count, err
}

func (t *Telemetry) ListAPISessions(ctx context.Context, query APISessionQuery) (APISessionListResult, error) {
	if err := t.Flush(ctx); err != nil {
		return APISessionListResult{}, err
	}
	db, err := t.currentDB()
	if err != nil {
		return APISessionListResult{}, err
	}
	activeWindow := query.ActiveWindow
	if activeWindow <= 0 {
		activeWindow = 5 * time.Minute
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	cutoff := time.Now().UTC().Add(-activeWindow)
	where := []string{}
	args := []any{}
	if value := strings.TrimSpace(query.APIKeyID); value != "" {
		where = append(where, "api_key_id = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Source); value != "" {
		where = append(where, "source = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Search); value != "" {
		where = append(where, "(device_id LIKE ? OR source LIKE ? OR api_key_name LIKE ?)")
		like := "%" + value + "%"
		args = append(args, like, like, like)
	}
	if !query.IncludeStale {
		where = append(where, "last_seen_at >= ?")
		args = append(args, formatTime(cutoff))
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*) FROM api_key_sessions %s
	`, whereSQL), args...).Scan(&total); err != nil {
		t.markQueryFailure(err)
		return APISessionListResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, api_key_name, key_prefix, device_id, source, first_seen_at,
		       last_seen_at, last_status_code, request_count, token_count
		FROM api_key_sessions
		%s
		ORDER BY last_seen_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL), queryArgs...)
	if err != nil {
		t.markQueryFailure(err)
		return APISessionListResult{}, err
	}
	defer rows.Close()
	items := []APISession{}
	for rows.Next() {
		var item APISession
		var firstSeenAt, lastSeenAt string
		if err := rows.Scan(&item.APIKeyID, &item.APIKeyName, &item.KeyPrefix, &item.DeviceID,
			&item.Source, &firstSeenAt, &lastSeenAt, &item.LastStatusCode, &item.RequestCount,
			&item.TokenCount); err != nil {
			return APISessionListResult{}, err
		}
		item.FirstSeenAt, err = parseTime(firstSeenAt)
		if err != nil {
			return APISessionListResult{}, err
		}
		item.LastSeenAt, err = parseTime(lastSeenAt)
		if err != nil {
			return APISessionListResult{}, err
		}
		item.Active = !item.LastSeenAt.Before(cutoff)
		items = append(items, item)
	}
	return APISessionListResult{Items: items, Total: total, Limit: limit, Offset: offset}, rows.Err()
}

func (t *Telemetry) Prune(ctx context.Context, now time.Time) error {
	db, err := t.currentDB()
	if err != nil {
		return err
	}
	cutoff := formatTime(now.UTC().Add(-t.retention))
	for _, target := range []struct {
		table  string
		column string
	}{
		{table: "request_logs", column: "created_at"},
		{table: "api_key_sessions", column: "last_seen_at"},
	} {
		for {
			result, err := db.ExecContext(ctx, fmt.Sprintf(`
				DELETE FROM %s
				WHERE rowid IN (
					SELECT rowid FROM %s WHERE %s < ? LIMIT 1000
				)
			`, target.table, target.table, target.column), cutoff)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected < 1000 {
				break
			}
		}
	}
	_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	return err
}

func (t *Telemetry) QuickCheck(ctx context.Context) error {
	db, err := t.currentDB()
	if err != nil {
		return err
	}
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("telemetry database quick_check: %s", result)
	}
	return nil
}

func migrateTelemetry(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			api_key_id TEXT NOT NULL,
			api_key_name TEXT NOT NULL DEFAULT '',
			key_prefix TEXT NOT NULL DEFAULT '',
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
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS api_key_sessions (
			api_key_id TEXT NOT NULL,
			api_key_name TEXT NOT NULL DEFAULT '',
			key_prefix TEXT NOT NULL DEFAULT '',
			device_id TEXT NOT NULL,
			source TEXT NOT NULL,
			first_seen_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			last_status_code INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			token_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(api_key_id, device_id, source)
		);
		CREATE INDEX IF NOT EXISTS idx_request_logs_api_key_created_at
			ON request_logs(api_key_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_request_logs_created_at
			ON request_logs(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_request_logs_provider_created_at
			ON request_logs(provider, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_api_key_sessions_last_seen
			ON api_key_sessions(last_seen_at DESC);
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`)
	return err
}
