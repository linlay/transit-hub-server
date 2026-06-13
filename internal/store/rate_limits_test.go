package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRateLimitStatusesIgnoreLogsOutsideFixedWindow(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "rate-limits.db"))
	defer closeTestStore(t, store)

	created, err := store.CreateAPIKey(t.Context(), CreateAPIKeyParams{
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
	now := time.Date(2026, 6, 12, 15, 30, 0, 0, loc)
	outside := time.Date(2026, 6, 12, 14, 59, 0, 0, loc)
	inside := time.Date(2026, 6, 12, 15, 1, 0, 0, loc)
	insertRequestLogForRateLimitTest(t, store, created.ID, outside, 5, 5, 100)
	insertRequestLogForRateLimitTest(t, store, created.ID, inside, 3, 4, 11)

	statuses, err := store.RateLimitStatuses(t.Context(), created.ID, created.RateLimits, now, loc)
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

func insertRequestLogForRateLimitTest(t *testing.T, store *Store, apiKeyID string, createdAt time.Time, requestTokens, responseTokens, costMicro int64) {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), `
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			status_code, latency_ms, request_tokens, response_tokens, cost_micro, created_at
		) VALUES (?, 'openai', 'public-model', 'upstream-model', 'provider-a', 'default', 'acct', 200, 10, ?, ?, ?, ?)
	`, apiKeyID, requestTokens, responseTokens, costMicro, formatTime(createdAt.UTC()))
	if err != nil {
		t.Fatal(err)
	}
}
