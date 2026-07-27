package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultUsageFlushInterval = time.Second
	defaultUsageFlushBatch    = int64(100)
)

var supportedRateLimitWindows = []string{
	RateLimitWindow1H,
	RateLimitWindow5H,
	RateLimitWindow1D,
	RateLimitWindow7D,
	RateLimitWindow30D,
}

type UsageTotal struct {
	UsedRequests int64
	UsedTokens   int64
	LastUsedAt   *time.Time
}

type usageBucketKey struct {
	APIKeyID    string
	Window      string
	WindowStart string
}

type usageBucketValue struct {
	Requests  int64
	Tokens    int64
	CostMicro int64
	UpdatedAt time.Time
}

type usageTotalDelta struct {
	Requests   int64
	Tokens     int64
	LastUsedAt time.Time
}

// UsageManager keeps the authoritative counters for the running single
// instance in memory and asynchronously checkpoints deltas to its own SQLite
// database. Database failures never block request admission.
type UsageManager struct {
	path string
	loc  *time.Location

	connectMu     sync.Mutex
	mu            sync.RWMutex
	db            *sql.DB
	totals        map[string]UsageTotal
	buckets       map[usageBucketKey]usageBucketValue
	dirtyTotals   map[string]usageTotalDelta
	dirtyBuckets  map[usageBucketKey]usageBucketValue
	pendingEvents int64

	degraded atomic.Bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewUsageManager(path string, loc *time.Location) (*UsageManager, error) {
	if loc == nil {
		loc = time.UTC
	}
	manager := &UsageManager{
		path:         path,
		loc:          loc,
		totals:       map[string]UsageTotal{},
		buckets:      map[usageBucketKey]usageBucketValue{},
		dirtyTotals:  map[string]usageTotalDelta{},
		dirtyBuckets: map[usageBucketKey]usageBucketValue{},
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	err := manager.connect()
	if err != nil {
		manager.degraded.Store(true)
	}
	go manager.run()
	return manager, err
}

func (u *UsageManager) Bootstrap(keys []APIKey) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, key := range keys {
		if _, exists := u.totals[key.ID]; exists {
			continue
		}
		total := UsageTotal{
			UsedRequests: key.UsedRequests,
			UsedTokens:   key.UsedTokens,
			LastUsedAt:   key.LastUsedAt,
		}
		u.totals[key.ID] = total
		if key.UsedRequests == 0 && key.UsedTokens == 0 && key.LastUsedAt == nil {
			continue
		}
		delta := usageTotalDelta{Requests: key.UsedRequests, Tokens: key.UsedTokens}
		if key.LastUsedAt != nil {
			delta.LastUsedAt = key.LastUsedAt.UTC()
		}
		u.dirtyTotals[key.ID] = delta
		u.pendingEvents++
	}
	u.signalIfNeededLocked()
}

func (u *UsageManager) Record(apiKeyID string, requestTokens, responseTokens, costMicro int64, at time.Time) {
	if apiKeyID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	tokenDelta := requestTokens + responseTokens

	u.mu.Lock()
	total := u.totals[apiKeyID]
	total.UsedRequests++
	total.UsedTokens += tokenDelta
	total.LastUsedAt = timePtr(at)
	u.totals[apiKeyID] = total

	totalDelta := u.dirtyTotals[apiKeyID]
	totalDelta.Requests++
	totalDelta.Tokens += tokenDelta
	if totalDelta.LastUsedAt.IsZero() || at.After(totalDelta.LastUsedAt) {
		totalDelta.LastUsedAt = at
	}
	u.dirtyTotals[apiKeyID] = totalDelta

	for _, window := range supportedRateLimitWindows {
		start, _, err := rateLimitWindowBounds(window, at, u.loc)
		if err != nil {
			continue
		}
		key := usageBucketKey{APIKeyID: apiKeyID, Window: window, WindowStart: formatTime(start)}
		value := u.buckets[key]
		value.Requests++
		value.Tokens += tokenDelta
		value.CostMicro += costMicro
		value.UpdatedAt = at
		u.buckets[key] = value

		dirty := u.dirtyBuckets[key]
		dirty.Requests++
		dirty.Tokens += tokenDelta
		dirty.CostMicro += costMicro
		dirty.UpdatedAt = at
		u.dirtyBuckets[key] = dirty
	}
	u.pendingEvents++
	u.signalIfNeededLocked()
	u.mu.Unlock()
}

func (u *UsageManager) Overlay(key APIKey) APIKey {
	u.mu.RLock()
	total, ok := u.totals[key.ID]
	u.mu.RUnlock()
	if !ok {
		return key
	}
	key.UsedRequests = total.UsedRequests
	key.UsedTokens = total.UsedTokens
	key.LastUsedAt = total.LastUsedAt
	return key
}

func (u *UsageManager) Total(apiKeyID string) UsageTotal {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.totals[apiKeyID]
}

func (u *UsageManager) RateLimitStatuses(apiKeyID string, limits []RateLimit, now time.Time) ([]RateLimitStatus, error) {
	normalized, err := NormalizeRateLimits(limits)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	statuses := make([]RateLimitStatus, 0, len(normalized))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for _, limit := range normalized {
		start, end, err := rateLimitWindowBounds(limit.Window, now, u.loc)
		if err != nil {
			return nil, err
		}
		value := u.buckets[usageBucketKey{
			APIKeyID:    apiKeyID,
			Window:      limit.Window,
			WindowStart: formatTime(start),
		}]
		status := RateLimitStatus{
			Window:         limit.Window,
			StartsAt:       start,
			ResetsAt:       end,
			Requests:       value.Requests,
			RequestQuota:   limit.RequestQuota,
			Tokens:         value.Tokens,
			TokenQuota:     limit.TokenQuota,
			CostMicro:      value.CostMicro,
			CostQuotaMicro: limit.CostQuotaMicro,
		}
		status.RequestRemaining = remaining(status.RequestQuota, status.Requests)
		status.TokenRemaining = remaining(status.TokenQuota, status.Tokens)
		status.CostRemainingMicro = remaining(status.CostQuotaMicro, status.CostMicro)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (u *UsageManager) PendingUpdates() int64 {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.pendingEvents
}

func (u *UsageManager) Degraded() bool {
	return u.degraded.Load()
}

func (u *UsageManager) Flush(ctx context.Context) error {
	totals, buckets, pending := u.takeDirty()
	if pending == 0 {
		return nil
	}
	if err := u.ensureConnected(); err != nil {
		u.restoreDirty(totals, buckets, pending)
		u.degraded.Store(true)
		return err
	}

	u.mu.RLock()
	db := u.db
	u.mu.RUnlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		u.restoreDirty(totals, buckets, pending)
		u.degraded.Store(true)
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for apiKeyID, delta := range totals {
		var lastUsed any
		if !delta.LastUsedAt.IsZero() {
			lastUsed = formatTime(delta.LastUsedAt)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_totals (api_key_id, used_requests, used_tokens, last_used_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id) DO UPDATE SET
				used_requests = usage_totals.used_requests + excluded.used_requests,
				used_tokens = usage_totals.used_tokens + excluded.used_tokens,
				last_used_at = CASE
					WHEN excluded.last_used_at IS NULL THEN usage_totals.last_used_at
					WHEN usage_totals.last_used_at IS NULL OR excluded.last_used_at > usage_totals.last_used_at THEN excluded.last_used_at
					ELSE usage_totals.last_used_at
				END,
				updated_at = excluded.updated_at
		`, apiKeyID, delta.Requests, delta.Tokens, lastUsed, formatTime(time.Now().UTC())); err != nil {
			u.restoreDirty(totals, buckets, pending)
			u.degraded.Store(true)
			return err
		}
	}
	for key, delta := range buckets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_buckets (
				api_key_id, window, window_start, requests, tokens, cost_micro, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id, window, window_start) DO UPDATE SET
				requests = usage_buckets.requests + excluded.requests,
				tokens = usage_buckets.tokens + excluded.tokens,
				cost_micro = usage_buckets.cost_micro + excluded.cost_micro,
				updated_at = excluded.updated_at
		`, key.APIKeyID, key.Window, key.WindowStart, delta.Requests, delta.Tokens, delta.CostMicro, formatTime(delta.UpdatedAt)); err != nil {
			u.restoreDirty(totals, buckets, pending)
			u.degraded.Store(true)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		u.restoreDirty(totals, buckets, pending)
		u.degraded.Store(true)
		return err
	}
	u.degraded.Store(false)
	return nil
}

func (u *UsageManager) Close(ctx context.Context) error {
	u.stopOnce.Do(func() { close(u.stop) })
	select {
	case <-u.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := u.Flush(ctx); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.db != nil {
		err := u.db.Close()
		u.db = nil
		return err
	}
	return nil
}

func (u *UsageManager) run() {
	defer close(u.done)
	ticker := time.NewTicker(defaultUsageFlushInterval)
	defer ticker.Stop()
	backoff := time.Second
	for {
		select {
		case <-ticker.C:
		case <-u.wake:
		case <-u.stop:
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := u.Flush(ctx)
		cancel()
		if err != nil {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-u.stop:
				timer.Stop()
				return
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = time.Second
	}
}

func (u *UsageManager) connect() error {
	u.connectMu.Lock()
	defer u.connectMu.Unlock()

	u.mu.RLock()
	connected := u.db != nil
	u.mu.RUnlock()
	if connected {
		return nil
	}

	db, err := openSQLite(u.path, time.Second)
	if err != nil {
		return err
	}
	if err := migrateUsage(db); err != nil {
		_ = db.Close()
		return err
	}
	persistedTotals, persistedBuckets, err := loadUsage(db, u.loc, time.Now().UTC())
	if err != nil {
		_ = db.Close()
		return err
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	for id, persisted := range persistedTotals {
		dirty := u.dirtyTotals[id]
		persisted.UsedRequests += dirty.Requests
		persisted.UsedTokens += dirty.Tokens
		if !dirty.LastUsedAt.IsZero() && (persisted.LastUsedAt == nil || dirty.LastUsedAt.After(*persisted.LastUsedAt)) {
			persisted.LastUsedAt = timePtr(dirty.LastUsedAt)
		}
		u.totals[id] = persisted
	}
	for key, persisted := range persistedBuckets {
		dirty := u.dirtyBuckets[key]
		persisted.Requests += dirty.Requests
		persisted.Tokens += dirty.Tokens
		persisted.CostMicro += dirty.CostMicro
		if dirty.UpdatedAt.After(persisted.UpdatedAt) {
			persisted.UpdatedAt = dirty.UpdatedAt
		}
		u.buckets[key] = persisted
	}
	u.db = db
	return nil
}

func (u *UsageManager) ensureConnected() error {
	u.mu.RLock()
	connected := u.db != nil
	u.mu.RUnlock()
	if connected {
		return nil
	}
	return u.connect()
}

func (u *UsageManager) takeDirty() (map[string]usageTotalDelta, map[usageBucketKey]usageBucketValue, int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	totals := u.dirtyTotals
	buckets := u.dirtyBuckets
	pending := u.pendingEvents
	u.dirtyTotals = map[string]usageTotalDelta{}
	u.dirtyBuckets = map[usageBucketKey]usageBucketValue{}
	u.pendingEvents = 0
	return totals, buckets, pending
}

func (u *UsageManager) restoreDirty(totals map[string]usageTotalDelta, buckets map[usageBucketKey]usageBucketValue, pending int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, delta := range totals {
		current := u.dirtyTotals[id]
		current.Requests += delta.Requests
		current.Tokens += delta.Tokens
		if delta.LastUsedAt.After(current.LastUsedAt) {
			current.LastUsedAt = delta.LastUsedAt
		}
		u.dirtyTotals[id] = current
	}
	for key, delta := range buckets {
		current := u.dirtyBuckets[key]
		current.Requests += delta.Requests
		current.Tokens += delta.Tokens
		current.CostMicro += delta.CostMicro
		if delta.UpdatedAt.After(current.UpdatedAt) {
			current.UpdatedAt = delta.UpdatedAt
		}
		u.dirtyBuckets[key] = current
	}
	u.pendingEvents += pending
}

func (u *UsageManager) signalIfNeededLocked() {
	if u.pendingEvents < defaultUsageFlushBatch {
		return
	}
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

func migrateUsage(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_totals (
			api_key_id TEXT PRIMARY KEY,
			used_requests INTEGER NOT NULL DEFAULT 0,
			used_tokens INTEGER NOT NULL DEFAULT 0,
			last_used_at TEXT,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS usage_buckets (
			api_key_id TEXT NOT NULL,
			window TEXT NOT NULL,
			window_start TEXT NOT NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			tokens INTEGER NOT NULL DEFAULT 0,
			cost_micro INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (api_key_id, window, window_start)
		);
		CREATE INDEX IF NOT EXISTS idx_usage_buckets_window_start
			ON usage_buckets(window, window_start);
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`)
	return err
}

func loadUsage(db *sql.DB, loc *time.Location, now time.Time) (map[string]UsageTotal, map[usageBucketKey]usageBucketValue, error) {
	totals := map[string]UsageTotal{}
	rows, err := db.Query(`SELECT api_key_id, used_requests, used_tokens, last_used_at FROM usage_totals`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var total UsageTotal
		var last sql.NullString
		if err := rows.Scan(&id, &total.UsedRequests, &total.UsedTokens, &last); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if last.Valid {
			parsed, err := parseTime(last.String)
			if err != nil {
				_ = rows.Close()
				return nil, nil, err
			}
			total.LastUsedAt = &parsed
		}
		totals[id] = total
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	buckets := map[usageBucketKey]usageBucketValue{}
	currentStarts := map[string]string{}
	for _, window := range supportedRateLimitWindows {
		start, _, err := rateLimitWindowBounds(window, now, loc)
		if err != nil {
			return nil, nil, err
		}
		currentStarts[window] = formatTime(start)
	}
	rows, err = db.Query(`
		SELECT api_key_id, window, window_start, requests, tokens, cost_micro, updated_at
		FROM usage_buckets
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key usageBucketKey
		var value usageBucketValue
		var updated string
		if err := rows.Scan(&key.APIKeyID, &key.Window, &key.WindowStart, &value.Requests, &value.Tokens, &value.CostMicro, &updated); err != nil {
			return nil, nil, err
		}
		value.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, nil, err
		}
		if currentStarts[key.Window] != key.WindowStart {
			continue
		}
		buckets[key] = value
	}
	return totals, buckets, rows.Err()
}

func timePtr(value time.Time) *time.Time {
	copy := value
	return &copy
}

func (u *UsageManager) QuickCheck(ctx context.Context) error {
	u.mu.RLock()
	db := u.db
	u.mu.RUnlock()
	if db == nil {
		return errors.New("usage database unavailable")
	}
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("usage database quick_check: %s", result)
	}
	return nil
}
