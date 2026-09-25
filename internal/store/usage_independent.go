package store

import (
	"context"
	"database/sql"
	"time"
)

// WindowBindings belongs to one admitted request and must not be mutated.
type WindowBindings map[string]time.Time
type windowKey struct{ APIKeyID, Window string }
type usageWindow struct{ Start, End time.Time }

func independentDuration(window string) time.Duration {
	switch window {
	case RateLimitWindow5H:
		return 5 * time.Hour
	case RateLimitWindow7D:
		return 7 * 24 * time.Hour
	}
	return 0
}

func (u *UsageManager) windowBoundsLocked(id, window string, now time.Time) (time.Time, time.Time, string, error) {
	if independentDuration(window) == 0 {
		start, end, err := rateLimitWindowBounds(window, now, u.loc)
		return start, end, "active", err
	}
	w, exists := u.windows[windowKey{id, window}]
	if !exists {
		return time.Time{}, time.Time{}, "idle", nil
	}
	if !now.Before(w.End) {
		return w.Start, w.End, "expired", nil
	}
	return w.Start, w.End, "active", nil
}

// Admit checks all quotas and atomically persists new independent windows.
// Usage remains a soft quota: token/cost counters are updated on completion.
func (u *UsageManager) Admit(ctx context.Context, key APIKey, now time.Time) (WindowBindings, error) {
	limits, err := NormalizeRateLimits(key.RateLimits)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	for _, limit := range limits {
		if independentDuration(limit.Window) > 0 {
			if err := u.ensureConnected(); err != nil {
				return nil, err
			}
			break
		}
	}
	// Match Flush's lock order; do not hold mu while waiting for a flush transaction.
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	if total, ok := u.totals[key.ID]; ok {
		key.UsedRequests, key.UsedTokens, key.UsedMicrocredits = total.UsedRequests, total.UsedTokens, total.UsedMicrocredits
	}
	if err := ValidateUsableKey(key, now); err != nil {
		return nil, err
	}
	statuses, err := u.rateLimitStatusesLocked(key.ID, limits, now)
	if err != nil {
		return nil, err
	}
	if violation, exhausted := FirstRateLimitViolation(statuses); exhausted {
		return nil, violation
	}
	bindings := WindowBindings{}
	changes := map[windowKey]usageWindow{}
	for _, limit := range limits {
		duration := independentDuration(limit.Window)
		if duration == 0 {
			continue
		}
		wk := windowKey{key.ID, limit.Window}
		w, exists := u.windows[wk]
		if !exists || !now.Before(w.End) {
			w = usageWindow{now, now.Add(duration)}
			changes[wk] = w
		}
		bindings[limit.Window] = w.Start
	}
	if len(changes) == 0 {
		return bindings, nil
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for wk, w := range changes {
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_windows(api_key_id, window, window_start, window_end, updated_at)
   VALUES (?, ?, ?, ?, ?) ON CONFLICT(api_key_id,window) DO UPDATE SET
   window_start=excluded.window_start, window_end=excluded.window_end, updated_at=excluded.updated_at`,
			wk.APIKeyID, wk.Window, formatTime(w.Start), formatTime(w.End), formatTime(now))
		if err != nil {
			return nil, err
		}
		// A legacy aligned bucket might have exactly the same timestamp. Start clean.
		if _, err = tx.ExecContext(ctx, `DELETE FROM usage_buckets WHERE api_key_id=? AND window=? AND window_start=?`, wk.APIKeyID, wk.Window, formatTime(w.Start)); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for wk, w := range changes {
		u.windows[wk] = w
	}
	return bindings, nil
}

func loadWindows(db *sql.DB) (map[windowKey]usageWindow, error) {
	rows, err := db.Query(`SELECT api_key_id, window, window_start, window_end FROM usage_windows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[windowKey]usageWindow{}
	for rows.Next() {
		var key windowKey
		var start, end string
		if err := rows.Scan(&key.APIKeyID, &key.Window, &start, &end); err != nil {
			return nil, err
		}
		s, err := parseTime(start)
		if err != nil {
			return nil, err
		}
		e, err := parseTime(end)
		if err != nil {
			return nil, err
		}
		result[key] = usageWindow{s, e}
	}
	return result, rows.Err()
}
