package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPriceNotFound = errors.New("model price not found")
var DefaultCurrency = "CNY"

type ModelPrice struct {
	ID                                string    `json:"id"`
	Protocol                          string    `json:"protocol"`
	PublicModel                       string    `json:"public_model"`
	InputCostMicroPer1MTokens         int64     `json:"input_cost_micro_per_1m_tokens"`
	InputCacheHitCostMicroPer1MTokens *int64    `json:"input_cache_hit_cost_micro_per_1m_tokens"`
	OutputCostMicroPer1MTokens        int64     `json:"output_cost_micro_per_1m_tokens"`
	Currency                          string    `json:"currency"`
	CreatedAt                         time.Time `json:"created_at"`
	UpdatedAt                         time.Time `json:"updated_at"`
}

type ModelPriceParams struct {
	Protocol                          string
	PublicModel                       string
	InputCostMicroPer1MTokens         int64
	InputCacheHitCostMicroPer1MTokens *int64
	OutputCostMicroPer1MTokens        int64
	Currency                          string
}

type Overview struct {
	TotalRequests  int64           `json:"total_requests"`
	TotalTokens    int64           `json:"total_tokens"`
	RequestTokens  int64           `json:"request_tokens"`
	ResponseTokens int64           `json:"response_tokens"`
	TotalCost      int64           `json:"total_cost_micro"`
	ErrorRequests  int64           `json:"error_requests"`
	AverageLatency float64         `json:"average_latency_ms"`
	ActiveDevices  int64           `json:"active_devices"`
	APIKeys        APIKeyCounts    `json:"api_keys"`
	RecentTraffic  []TrafficBucket `json:"recent_traffic"`
	RiskKeys       []APIKeyRisk    `json:"risk_keys"`
}

type APIKeyCounts struct {
	Total    int64 `json:"total"`
	Active   int64 `json:"active"`
	Disabled int64 `json:"disabled"`
	Deleted  int64 `json:"deleted"`
}

type APIKeyRisk struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	KeyPrefix        string  `json:"key_prefix"`
	RequestRemaining int64   `json:"request_remaining"`
	TokenRemaining   int64   `json:"token_remaining"`
	RequestUsedRatio float64 `json:"request_used_ratio"`
	TokenUsedRatio   float64 `json:"token_used_ratio"`
}

type TrafficQuery struct {
	APIKeyID string
	From     *time.Time
	To       *time.Time
	Bucket   string
	Limit    int
	Offset   int
}

type TrafficBucket struct {
	Bucket           string   `json:"bucket"`
	Requests         int64    `json:"requests"`
	RequestTokens    int64    `json:"request_tokens"`
	ResponseTokens   int64    `json:"response_tokens"`
	TotalTokens      int64    `json:"total_tokens"`
	CacheHitTokens   int64    `json:"cache_hit_tokens"`
	CacheMissTokens  int64    `json:"cache_miss_tokens"`
	CacheTotalTokens int64    `json:"cache_total_tokens"`
	CacheHitRate     *float64 `json:"cache_hit_rate"`
	CostMicro        int64    `json:"cost_micro"`
	ErrorRequests    int64    `json:"error_requests"`
	AverageLatency   float64  `json:"average_latency_ms"`
}

type RequestLogQuery struct {
	APIKeyID string
	From     *time.Time
	To       *time.Time
	Limit    int
	Offset   int
}

type RequestLogEntry struct {
	ID               int64     `json:"id"`
	APIKeyID         string    `json:"api_key_id"`
	APIKeyName       string    `json:"api_key_name"`
	Protocol         string    `json:"protocol"`
	PublicModel      string    `json:"public_model"`
	UpstreamModel    string    `json:"upstream_model"`
	Provider         string    `json:"provider"`
	Pool             string    `json:"pool"`
	Account          string    `json:"account"`
	DeviceID         string    `json:"device_id"`
	Source           string    `json:"source"`
	StatusCode       int       `json:"status_code"`
	LatencyMS        int64     `json:"latency_ms"`
	RequestTokens    int64     `json:"request_tokens"`
	ResponseTokens   int64     `json:"response_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	CacheHitTokens   int64     `json:"cache_hit_tokens"`
	CacheMissTokens  int64     `json:"cache_miss_tokens"`
	CacheTotalTokens int64     `json:"cache_total_tokens"`
	CacheHitRate     *float64  `json:"cache_hit_rate"`
	CostMicro        int64     `json:"cost_micro"`
	Estimated        bool      `json:"estimated"`
	ErrorType        string    `json:"error_type"`
	CreatedAt        time.Time `json:"created_at"`
}

type RequestLogListResult struct {
	Items  []RequestLogEntry `json:"items"`
	Total  int64             `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

type ProviderUsageQuery struct {
	From *time.Time
	To   *time.Time
}

type ProviderUsage struct {
	Provider         string   `json:"provider"`
	Requests         int64    `json:"requests"`
	RequestTokens    int64    `json:"request_tokens"`
	ResponseTokens   int64    `json:"response_tokens"`
	TotalTokens      int64    `json:"total_tokens"`
	CacheHitTokens   int64    `json:"cache_hit_tokens"`
	CacheMissTokens  int64    `json:"cache_miss_tokens"`
	CacheTotalTokens int64    `json:"cache_total_tokens"`
	CacheHitRate     *float64 `json:"cache_hit_rate"`
	CostMicro        int64    `json:"cost_micro"`
	ErrorRequests    int64    `json:"error_requests"`
	AverageLatency   float64  `json:"average_latency_ms"`
}

type ProviderAccountUsage struct {
	Provider       string `json:"provider"`
	Pool           string `json:"pool"`
	Account        string `json:"account"`
	Requests       int64  `json:"requests"`
	RequestTokens  int64  `json:"request_tokens"`
	ResponseTokens int64  `json:"response_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	ErrorRequests  int64  `json:"error_requests"`
}

type APISessionQuery struct {
	APIKeyID     string
	Search       string
	Source       string
	IncludeStale bool
	ActiveWindow time.Duration
	Limit        int
	Offset       int
}

type APISession struct {
	APIKeyID       string    `json:"api_key_id"`
	APIKeyName     string    `json:"api_key_name"`
	KeyPrefix      string    `json:"key_prefix"`
	DeviceID       string    `json:"device_id"`
	Source         string    `json:"source"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Active         bool      `json:"active"`
	LastStatusCode int       `json:"last_status_code"`
	RequestCount   int64     `json:"request_count"`
	TokenCount     int64     `json:"token_count"`
}

type APISessionListResult struct {
	Items  []APISession `json:"items"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func (s *Store) UpsertModelPrice(ctx context.Context, params ModelPriceParams) (ModelPrice, error) {
	protocol := strings.ToLower(strings.TrimSpace(params.Protocol))
	publicModel := strings.TrimSpace(params.PublicModel)
	if protocol == "" || publicModel == "" {
		return ModelPrice{}, errors.New("protocol and public_model are required")
	}
	if params.InputCostMicroPer1MTokens < 0 || params.OutputCostMicroPer1MTokens < 0 || negativePtr(params.InputCacheHitCostMicroPer1MTokens) {
		return ModelPrice{}, errors.New("cost values must be >= 0")
	}
	currency := strings.ToUpper(strings.TrimSpace(params.Currency))
	if currency == "" {
		currency = DefaultCurrency
	}
	now := time.Now().UTC()
	id := newID("price")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			id, protocol, public_model, input_cost_micro_per_1m,
			input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
			currency, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(protocol, public_model) DO UPDATE SET
			input_cost_micro_per_1m = excluded.input_cost_micro_per_1m,
			input_cache_hit_cost_micro_per_1m = excluded.input_cache_hit_cost_micro_per_1m,
			output_cost_micro_per_1m = excluded.output_cost_micro_per_1m,
			currency = excluded.currency,
			updated_at = excluded.updated_at
	`, id, protocol, publicModel, params.InputCostMicroPer1MTokens, nullableInt64(params.InputCacheHitCostMicroPer1MTokens), params.OutputCostMicroPer1MTokens, currency, formatTime(now), formatTime(now))
	if err != nil {
		return ModelPrice{}, err
	}
	price, _, err := s.GetModelPrice(ctx, protocol, publicModel)
	return price, err
}

func (s *Store) UpdateModelPrice(ctx context.Context, id string, params ModelPriceParams) (ModelPrice, error) {
	current, err := s.GetModelPriceByID(ctx, id)
	if err != nil {
		return ModelPrice{}, err
	}
	protocol := strings.ToLower(strings.TrimSpace(params.Protocol))
	if protocol == "" {
		protocol = current.Protocol
	}
	publicModel := strings.TrimSpace(params.PublicModel)
	if publicModel == "" {
		publicModel = current.PublicModel
	}
	currency := strings.ToUpper(strings.TrimSpace(params.Currency))
	if currency == "" {
		currency = current.Currency
	}
	if params.InputCostMicroPer1MTokens < 0 || params.OutputCostMicroPer1MTokens < 0 || negativePtr(params.InputCacheHitCostMicroPer1MTokens) {
		return ModelPrice{}, errors.New("cost values must be >= 0")
	}
	inputCacheHitCost := current.InputCacheHitCostMicroPer1MTokens
	if params.InputCacheHitCostMicroPer1MTokens != nil {
		inputCacheHitCost = params.InputCacheHitCostMicroPer1MTokens
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE model_prices
		SET protocol = ?, public_model = ?, input_cost_micro_per_1m = ?,
		    input_cache_hit_cost_micro_per_1m = ?, output_cost_micro_per_1m = ?,
		    currency = ?, updated_at = ?
		WHERE id = ?
	`, protocol, publicModel, params.InputCostMicroPer1MTokens, nullableInt64(inputCacheHitCost), params.OutputCostMicroPer1MTokens, currency, formatTime(now), id)
	if err != nil {
		return ModelPrice{}, err
	}
	return s.GetModelPriceByID(ctx, id)
}

func (s *Store) DeleteModelPrice(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM model_prices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrPriceNotFound
	}
	return nil
}

func (s *Store) ListModelPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, protocol, public_model, input_cost_micro_per_1m,
		       input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
		       currency, created_at, updated_at
		FROM model_prices
		ORDER BY protocol ASC, public_model ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := []ModelPrice{}
	for rows.Next() {
		price, err := scanModelPrice(rows)
		if err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}
	return prices, rows.Err()
}

func (s *Store) GetModelPrice(ctx context.Context, protocol, publicModel string) (ModelPrice, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, protocol, public_model, input_cost_micro_per_1m,
		       input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
		       currency, created_at, updated_at
		FROM model_prices
		WHERE protocol = ? AND public_model = ?
	`, strings.ToLower(strings.TrimSpace(protocol)), strings.TrimSpace(publicModel))
	price, err := scanModelPrice(row)
	if errors.Is(err, ErrPriceNotFound) {
		return ModelPrice{}, false, nil
	}
	return price, true, err
}

func (s *Store) GetModelPriceByID(ctx context.Context, id string) (ModelPrice, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, protocol, public_model, input_cost_micro_per_1m,
		       input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
		       currency, created_at, updated_at
		FROM model_prices
		WHERE id = ?
	`, id)
	return scanModelPrice(row)
}

func (s *Store) EstimateCost(ctx context.Context, protocol, publicModel string, requestTokens, responseTokens, cacheHitTokens, cacheMissTokens int64) (int64, error) {
	price, ok, err := s.GetModelPrice(ctx, protocol, publicModel)
	if err != nil || !ok {
		return 0, err
	}
	if price.InputCacheHitCostMicroPer1MTokens != nil && cacheHitTokens+cacheMissTokens > 0 {
		normalInputTokens := cacheMissTokens
		if remainder := requestTokens - cacheHitTokens - cacheMissTokens; remainder > 0 {
			normalInputTokens += remainder
		}
		return (cacheHitTokens*(*price.InputCacheHitCostMicroPer1MTokens) + normalInputTokens*price.InputCostMicroPer1MTokens + responseTokens*price.OutputCostMicroPer1MTokens) / 1_000_000, nil
	}
	return (requestTokens*price.InputCostMicroPer1MTokens + responseTokens*price.OutputCostMicroPer1MTokens) / 1_000_000, nil
}

func (s *Store) Overview(ctx context.Context, activeWindow time.Duration) (Overview, error) {
	var overview Overview
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(request_tokens), 0),
			COALESCE(SUM(response_tokens), 0),
			COALESCE(SUM(cost_micro), 0),
			COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM request_logs
	`).Scan(&overview.TotalRequests, &overview.RequestTokens, &overview.ResponseTokens, &overview.TotalCost, &overview.ErrorRequests, &overview.AverageLatency); err != nil {
		return Overview{}, err
	}
	overview.TotalTokens = overview.RequestTokens + overview.ResponseTokens
	counts, err := s.apiKeyCounts(ctx)
	if err != nil {
		return Overview{}, err
	}
	overview.APIKeys = counts
	activeDevices, err := s.CountActiveSessions(ctx, activeWindow)
	if err != nil {
		return Overview{}, err
	}
	overview.ActiveDevices = activeDevices
	from := time.Now().UTC().Add(-14 * 24 * time.Hour)
	recent, err := s.Traffic(ctx, TrafficQuery{From: &from, Bucket: "day"})
	if err != nil {
		return Overview{}, err
	}
	overview.RecentTraffic = recent
	risks, err := s.quotaRiskKeys(ctx)
	if err != nil {
		return Overview{}, err
	}
	overview.RiskKeys = risks
	return overview, nil
}

func (s *Store) Traffic(ctx context.Context, query TrafficQuery) ([]TrafficBucket, error) {
	bucketExpr := `substr(created_at, 1, 10)`
	if strings.EqualFold(query.Bucket, "hour") {
		bucketExpr = `substr(created_at, 1, 13)`
	}
	where, args := requestLogWhere(query.APIKeyID, query.From, query.To)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS bucket,
		       COUNT(*) AS requests,
		       COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(cache_hit_tokens), 0),
		       COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucketExpr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := []TrafficBucket{}
	for rows.Next() {
		var bucket TrafficBucket
		if err := rows.Scan(&bucket.Bucket, &bucket.Requests, &bucket.RequestTokens, &bucket.ResponseTokens, &bucket.CacheHitTokens, &bucket.CacheMissTokens, &bucket.CostMicro, &bucket.ErrorRequests, &bucket.AverageLatency); err != nil {
			return nil, err
		}
		fillTrafficDerived(&bucket)
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (s *Store) APIKeyUsage(ctx context.Context, apiKeyID string, activeWindow time.Duration, now time.Time, loc *time.Location) (map[string]any, error) {
	key, err := s.GetAPIKey(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	var summary TrafficBucket
	where, args := requestLogWhere(apiKeyID, nil, nil)
	err = s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*),
		       COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(cache_hit_tokens), 0),
		       COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
	`, where), args...).Scan(&summary.Requests, &summary.RequestTokens, &summary.ResponseTokens, &summary.CacheHitTokens, &summary.CacheMissTokens, &summary.CostMicro, &summary.ErrorRequests, &summary.AverageLatency)
	if err != nil {
		return nil, err
	}
	fillTrafficDerived(&summary)
	recentFrom := time.Now().UTC().Add(-14 * 24 * time.Hour)
	traffic, err := s.Traffic(ctx, TrafficQuery{APIKeyID: apiKeyID, From: &recentFrom, Bucket: "day"})
	if err != nil {
		return nil, err
	}
	sessions, err := s.ListAPISessions(ctx, APISessionQuery{APIKeyID: apiKeyID, ActiveWindow: activeWindow, Limit: 200})
	if err != nil {
		return nil, err
	}
	activeDevices := int64(0)
	for _, session := range sessions.Items {
		if session.Active {
			activeDevices++
		}
	}
	rateLimitUsage, err := s.RateLimitStatuses(ctx, key.ID, key.RateLimits, now, loc)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"key":              key,
		"summary":          summary,
		"recent_traffic":   traffic,
		"active_devices":   activeDevices,
		"rate_limit_usage": rateLimitUsage,
	}, nil
}

func (s *Store) ListRequestLogs(ctx context.Context, query RequestLogQuery) (RequestLogListResult, error) {
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
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM request_logs %s`, where), args...).Scan(&total); err != nil {
		return RequestLogListResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT l.id, l.api_key_id, COALESCE(k.name, ''), l.protocol, l.public_model, l.upstream_model,
		       l.provider, l.pool, l.account, l.device_id, l.source, l.status_code, l.latency_ms,
		       l.request_tokens, l.response_tokens, l.cache_hit_tokens, l.cache_miss_tokens,
		       l.cost_micro, l.estimated, l.error_type, l.created_at
		FROM request_logs l
		LEFT JOIN api_keys k ON k.id = l.api_key_id
		%s
		ORDER BY l.created_at DESC, l.id DESC
		LIMIT ? OFFSET ?
	`, qualifyRequestLogWhere(where)), queryArgs...)
	if err != nil {
		return RequestLogListResult{}, err
	}
	defer rows.Close()
	items := []RequestLogEntry{}
	for rows.Next() {
		var item RequestLogEntry
		var estimated int
		var createdAt string
		if err := rows.Scan(&item.ID, &item.APIKeyID, &item.APIKeyName, &item.Protocol, &item.PublicModel, &item.UpstreamModel, &item.Provider, &item.Pool, &item.Account, &item.DeviceID, &item.Source, &item.StatusCode, &item.LatencyMS, &item.RequestTokens, &item.ResponseTokens, &item.CacheHitTokens, &item.CacheMissTokens, &item.CostMicro, &estimated, &item.ErrorType, &createdAt); err != nil {
			return RequestLogListResult{}, err
		}
		item.Estimated = estimated != 0
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		item.CacheTotalTokens = item.CacheHitTokens + item.CacheMissTokens
		item.CacheHitRate = cacheHitRate(item.CacheHitTokens, item.CacheMissTokens)
		parsed, err := parseTime(createdAt)
		if err != nil {
			return RequestLogListResult{}, err
		}
		item.CreatedAt = parsed
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RequestLogListResult{}, err
	}
	return RequestLogListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *Store) ProviderUsage(ctx context.Context, query ProviderUsageQuery) ([]ProviderUsage, error) {
	where, args := requestLogWhere("", query.From, query.To)
	if where == "" {
		where = "WHERE provider <> ''"
	} else {
		where += " AND provider <> ''"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider,
		       COUNT(*) AS requests,
		       COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(cache_hit_tokens), 0),
		       COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_micro), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
		GROUP BY provider
		ORDER BY requests DESC, provider ASC
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ProviderUsage{}
	for rows.Next() {
		var item ProviderUsage
		if err := rows.Scan(&item.Provider, &item.Requests, &item.RequestTokens, &item.ResponseTokens, &item.CacheHitTokens, &item.CacheMissTokens, &item.CostMicro, &item.ErrorRequests, &item.AverageLatency); err != nil {
			return nil, err
		}
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		item.CacheTotalTokens = item.CacheHitTokens + item.CacheMissTokens
		item.CacheHitRate = cacheHitRate(item.CacheHitTokens, item.CacheMissTokens)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ProviderAccountUsage(ctx context.Context, query ProviderUsageQuery) ([]ProviderAccountUsage, error) {
	where, args := requestLogWhere("", query.From, query.To)
	if where == "" {
		where = "WHERE provider <> '' AND account <> ''"
	} else {
		where += " AND provider <> '' AND account <> ''"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider,
		       pool,
		       account,
		       COUNT(*) AS requests,
		       COALESCE(SUM(request_tokens), 0),
		       COALESCE(SUM(response_tokens), 0),
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END), 0)
		FROM request_logs
		%s
		GROUP BY provider, pool, account
		ORDER BY provider ASC, pool ASC, requests DESC, account ASC
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ProviderAccountUsage{}
	for rows.Next() {
		var item ProviderAccountUsage
		if err := rows.Scan(&item.Provider, &item.Pool, &item.Account, &item.Requests, &item.RequestTokens, &item.ResponseTokens, &item.ErrorRequests); err != nil {
			return nil, err
		}
		item.TotalTokens = item.RequestTokens + item.ResponseTokens
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CountActiveSessions(ctx context.Context, activeWindow time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-activeWindow)
	var count int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM api_key_sessions
		WHERE last_seen_at >= ?
	`, formatTime(cutoff)).Scan(&count)
	return count, err
}

func (s *Store) ListAPISessions(ctx context.Context, query APISessionQuery) (APISessionListResult, error) {
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
	if strings.TrimSpace(query.APIKeyID) != "" {
		where = append(where, "s.api_key_id = ?")
		args = append(args, strings.TrimSpace(query.APIKeyID))
	}
	if strings.TrimSpace(query.Source) != "" {
		where = append(where, "s.source = ?")
		args = append(args, strings.TrimSpace(query.Source))
	}
	if strings.TrimSpace(query.Search) != "" {
		where = append(where, "(s.device_id LIKE ? OR s.source LIKE ? OR k.name LIKE ?)")
		like := "%" + strings.TrimSpace(query.Search) + "%"
		args = append(args, like, like, like)
	}
	if !query.IncludeStale {
		where = append(where, "s.last_seen_at >= ?")
		args = append(args, formatTime(cutoff))
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM api_key_sessions s
		JOIN api_keys k ON k.id = s.api_key_id
		%s
	`, whereSQL), args...).Scan(&total); err != nil {
		return APISessionListResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT s.api_key_id, k.name, k.key_prefix, s.device_id, s.source,
		       s.first_seen_at, s.last_seen_at, s.last_status_code,
		       s.request_count, s.token_count
		FROM api_key_sessions s
		JOIN api_keys k ON k.id = s.api_key_id
		%s
		ORDER BY s.last_seen_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL), queryArgs...)
	if err != nil {
		return APISessionListResult{}, err
	}
	defer rows.Close()

	items := []APISession{}
	for rows.Next() {
		var item APISession
		var firstSeenAt, lastSeenAt string
		if err := rows.Scan(&item.APIKeyID, &item.APIKeyName, &item.KeyPrefix, &item.DeviceID, &item.Source, &firstSeenAt, &lastSeenAt, &item.LastStatusCode, &item.RequestCount, &item.TokenCount); err != nil {
			return APISessionListResult{}, err
		}
		parsedFirst, err := parseTime(firstSeenAt)
		if err != nil {
			return APISessionListResult{}, err
		}
		parsedLast, err := parseTime(lastSeenAt)
		if err != nil {
			return APISessionListResult{}, err
		}
		item.FirstSeenAt = parsedFirst
		item.LastSeenAt = parsedLast
		item.Active = !parsedLast.Before(cutoff)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return APISessionListResult{}, err
	}
	return APISessionListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *Store) apiKeyCounts(ctx context.Context) (APIKeyCounts, error) {
	var counts APIKeyCounts
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN deleted_at IS NULL AND status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN deleted_at IS NULL AND status = 'disabled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM api_keys
	`).Scan(&counts.Total, &counts.Active, &counts.Disabled, &counts.Deleted); err != nil {
		return APIKeyCounts{}, err
	}
	return counts, nil
}

func (s *Store) quotaRiskKeys(ctx context.Context) ([]APIKeyRisk, error) {
	keys, err := s.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	risks := []APIKeyRisk{}
	for _, key := range keys {
		risk := APIKeyRisk{ID: key.ID, Name: key.Name, KeyPrefix: key.KeyPrefix}
		highRisk := false
		if key.RequestQuota > 0 {
			risk.RequestRemaining = key.RequestQuota - key.UsedRequests
			if risk.RequestRemaining < 0 {
				risk.RequestRemaining = 0
			}
			risk.RequestUsedRatio = float64(key.UsedRequests) / float64(key.RequestQuota)
			highRisk = highRisk || risk.RequestUsedRatio >= 0.8
		}
		if key.TokenQuota > 0 {
			risk.TokenRemaining = key.TokenQuota - key.UsedTokens
			if risk.TokenRemaining < 0 {
				risk.TokenRemaining = 0
			}
			risk.TokenUsedRatio = float64(key.UsedTokens) / float64(key.TokenQuota)
			highRisk = highRisk || risk.TokenUsedRatio >= 0.8
		}
		if highRisk {
			risks = append(risks, risk)
		}
		if len(risks) >= 8 {
			break
		}
	}
	return risks, nil
}

func requestLogWhere(apiKeyID string, from, to *time.Time) (string, []any) {
	where := []string{}
	args := []any{}
	if strings.TrimSpace(apiKeyID) != "" {
		where = append(where, "api_key_id = ?")
		args = append(args, strings.TrimSpace(apiKeyID))
	}
	if from != nil {
		where = append(where, "created_at >= ?")
		args = append(args, formatTime(from.UTC()))
	}
	if to != nil {
		where = append(where, "created_at <= ?")
		args = append(args, formatTime(to.UTC()))
	}
	if len(where) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

func fillTrafficDerived(bucket *TrafficBucket) {
	bucket.TotalTokens = bucket.RequestTokens + bucket.ResponseTokens
	bucket.CacheTotalTokens = bucket.CacheHitTokens + bucket.CacheMissTokens
	bucket.CacheHitRate = cacheHitRate(bucket.CacheHitTokens, bucket.CacheMissTokens)
}

func cacheHitRate(hit, miss int64) *float64 {
	total := hit + miss
	if total <= 0 {
		return nil
	}
	value := float64(hit) / float64(total)
	return &value
}

func qualifyRequestLogWhere(where string) string {
	replacer := strings.NewReplacer(
		"api_key_id", "l.api_key_id",
		"created_at", "l.created_at",
	)
	return replacer.Replace(where)
}

type modelPriceScanner interface {
	Scan(dest ...any) error
}

func scanModelPrice(scanner modelPriceScanner) (ModelPrice, error) {
	var price ModelPrice
	var inputCacheHitCost sql.NullInt64
	var createdAt, updatedAt string
	err := scanner.Scan(
		&price.ID,
		&price.Protocol,
		&price.PublicModel,
		&price.InputCostMicroPer1MTokens,
		&inputCacheHitCost,
		&price.OutputCostMicroPer1MTokens,
		&price.Currency,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelPrice{}, ErrPriceNotFound
	}
	if err != nil {
		return ModelPrice{}, err
	}
	if inputCacheHitCost.Valid {
		price.InputCacheHitCostMicroPer1MTokens = &inputCacheHitCost.Int64
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return ModelPrice{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return ModelPrice{}, err
	}
	price.CreatedAt = parsedCreatedAt
	price.UpdatedAt = parsedUpdatedAt
	return price, nil
}
