package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	RateLimitWindow1H  = "1h"
	RateLimitWindow5H  = "5h"
	RateLimitWindow1D  = "1d"
	RateLimitWindow7D  = "7d"
	RateLimitWindow30D = "30d"
)

var rateLimitWindowOrder = map[string]int{
	RateLimitWindow1H:  0,
	RateLimitWindow5H:  1,
	RateLimitWindow1D:  2,
	RateLimitWindow7D:  3,
	RateLimitWindow30D: 4,
}

type RateLimit struct {
	Window         string `json:"window"`
	RequestQuota   int64  `json:"request_quota"`
	TokenQuota     int64  `json:"token_quota"`
	CostQuotaMicro int64  `json:"cost_quota_micro"`
}

type RateLimitStatus struct {
	Window             string    `json:"window"`
	StartsAt           time.Time `json:"starts_at"`
	ResetsAt           time.Time `json:"resets_at"`
	Requests           int64     `json:"requests"`
	RequestQuota       int64     `json:"request_quota"`
	RequestRemaining   int64     `json:"request_remaining"`
	Tokens             int64     `json:"tokens"`
	TokenQuota         int64     `json:"token_quota"`
	TokenRemaining     int64     `json:"token_remaining"`
	CostMicro          int64     `json:"cost_micro"`
	CostQuotaMicro     int64     `json:"cost_quota_micro"`
	CostRemainingMicro int64     `json:"cost_remaining_micro"`
}

type RateLimitViolation struct {
	Window    string
	Dimension string
	ResetsAt  time.Time
}

func (v RateLimitViolation) Error() string {
	return fmt.Sprintf("api key %s rate limit exhausted for %s", v.Window, v.Dimension)
}

func NormalizeRateLimits(limits []RateLimit) ([]RateLimit, error) {
	normalized := make([]RateLimit, 0, len(limits))
	seen := map[string]struct{}{}
	for _, limit := range limits {
		window := strings.ToLower(strings.TrimSpace(limit.Window))
		if _, ok := rateLimitWindowOrder[window]; !ok {
			return nil, fmt.Errorf("unsupported rate limit window: %s", limit.Window)
		}
		if _, exists := seen[window]; exists {
			return nil, fmt.Errorf("duplicate rate limit window: %s", window)
		}
		seen[window] = struct{}{}
		if limit.RequestQuota < 0 || limit.TokenQuota < 0 || limit.CostQuotaMicro < 0 {
			return nil, errors.New("rate limit quotas must be >= 0")
		}
		normalized = append(normalized, RateLimit{
			Window:         window,
			RequestQuota:   limit.RequestQuota,
			TokenQuota:     limit.TokenQuota,
			CostQuotaMicro: limit.CostQuotaMicro,
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return rateLimitWindowOrder[normalized[i].Window] < rateLimitWindowOrder[normalized[j].Window]
	})
	return normalized, nil
}

func RateLimitsNeedCost(limits []RateLimit) bool {
	for _, limit := range limits {
		if limit.CostQuotaMicro > 0 {
			return true
		}
	}
	return false
}

func encodeRateLimits(limits []RateLimit) (string, error) {
	normalized, err := NormalizeRateLimits(limits)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeRateLimits(value string) ([]RateLimit, error) {
	if strings.TrimSpace(value) == "" {
		return []RateLimit{}, nil
	}
	var limits []RateLimit
	if err := json.Unmarshal([]byte(value), &limits); err != nil {
		return nil, err
	}
	return NormalizeRateLimits(limits)
}

func (s *Store) RateLimitStatuses(ctx context.Context, apiKeyID string, limits []RateLimit, now time.Time, loc *time.Location) ([]RateLimitStatus, error) {
	normalized, err := NormalizeRateLimits(limits)
	if err != nil {
		return nil, err
	}
	statuses := make([]RateLimitStatus, 0, len(normalized))
	for _, limit := range normalized {
		start, end, err := rateLimitWindowBounds(limit.Window, now, loc)
		if err != nil {
			return nil, err
		}
		status := RateLimitStatus{
			Window:         limit.Window,
			StartsAt:       start,
			ResetsAt:       end,
			RequestQuota:   limit.RequestQuota,
			TokenQuota:     limit.TokenQuota,
			CostQuotaMicro: limit.CostQuotaMicro,
		}
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(request_tokens + response_tokens), 0),
			       COALESCE(SUM(cost_micro), 0)
			FROM request_logs
			WHERE api_key_id = ? AND created_at >= ? AND created_at < ?
		`, strings.TrimSpace(apiKeyID), formatTime(start.UTC()), formatTime(end.UTC())).Scan(&status.Requests, &status.Tokens, &status.CostMicro); err != nil {
			return nil, err
		}
		status.RequestRemaining = remaining(status.RequestQuota, status.Requests)
		status.TokenRemaining = remaining(status.TokenQuota, status.Tokens)
		status.CostRemainingMicro = remaining(status.CostQuotaMicro, status.CostMicro)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func FirstRateLimitViolation(statuses []RateLimitStatus) (RateLimitViolation, bool) {
	for _, status := range statuses {
		if status.RequestQuota > 0 && status.Requests >= status.RequestQuota {
			return RateLimitViolation{Window: status.Window, Dimension: "requests", ResetsAt: status.ResetsAt}, true
		}
		if status.TokenQuota > 0 && status.Tokens >= status.TokenQuota {
			return RateLimitViolation{Window: status.Window, Dimension: "tokens", ResetsAt: status.ResetsAt}, true
		}
		if status.CostQuotaMicro > 0 && status.CostMicro >= status.CostQuotaMicro {
			return RateLimitViolation{Window: status.Window, Dimension: "cost", ResetsAt: status.ResetsAt}, true
		}
	}
	return RateLimitViolation{}, false
}

func rateLimitWindowBounds(window string, now time.Time, loc *time.Location) (time.Time, time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	localNow := now.In(loc)
	switch strings.ToLower(strings.TrimSpace(window)) {
	case RateLimitWindow1H:
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), localNow.Hour(), 0, 0, 0, loc)
		return start.UTC(), start.Add(time.Hour).UTC(), nil
	case RateLimitWindow5H:
		start, end := fixedDurationWindowBounds(localNow, loc, 5*time.Hour)
		return start, end, nil
	case RateLimitWindow1D:
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
		return start.UTC(), start.AddDate(0, 0, 1).UTC(), nil
	case RateLimitWindow7D:
		dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
		daysSinceMonday := (int(dayStart.Weekday()) + 6) % 7
		start := dayStart.AddDate(0, 0, -daysSinceMonday)
		return start.UTC(), start.AddDate(0, 0, 7).UTC(), nil
	case RateLimitWindow30D:
		start, end := fixedDurationWindowBounds(localNow, loc, 30*24*time.Hour)
		return start, end, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported rate limit window: %s", window)
	}
}

func fixedDurationWindowBounds(localNow time.Time, loc *time.Location, duration time.Duration) (time.Time, time.Time) {
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, loc)
	elapsed := localNow.Sub(epoch)
	bucket := elapsed / duration
	start := epoch.Add(bucket * duration)
	return start.UTC(), start.Add(duration).UTC()
}

func remaining(quota, used int64) int64 {
	if quota <= 0 {
		return 0
	}
	left := quota - used
	if left < 0 {
		return 0
	}
	return left
}
