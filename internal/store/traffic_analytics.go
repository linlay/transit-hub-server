package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type TrafficFilters struct {
	APIKeyIDs    []string
	Models       []string
	Provider     string
	Status       string
	ExclusiveEnd bool
}

func filteredRequestLogWhere(key string, from, to *time.Time, filters TrafficFilters) (string, []any) {
	where, args := requestLogWhere(key, from, to)
	if filters.ExclusiveEnd {
		where, args = requestLogWhere(key, nil, nil)
	}
	add := func(clause string, values ...any) {
		if where == "" {
			where = "WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, values...)
	}
	for _, group := range []struct {
		column string
		values []string
	}{{"api_key_id", filters.APIKeyIDs}, {"public_model", filters.Models}} {
		if len(group.values) == 0 {
			continue
		}
		params := make([]string, len(group.values))
		values := make([]any, len(group.values))
		for i, v := range group.values {
			params[i] = "?"
			values[i] = v
		}
		add(group.column+" IN ("+strings.Join(params, ",")+")", values...)
	}
	if filters.Provider != "" {
		add("provider = ?", filters.Provider)
	}
	if filters.Status == "failed" {
		add("(status_code >= 400 OR error_type <> '')")
	}
	if filters.Status == "success" {
		add("(status_code < 400 AND error_type = '')")
	}
	if filters.ExclusiveEnd {
		// RFC3339Nano has variable fractional precision; textual comparison is unsafe at exact boundaries.
		if from != nil {
			add("julianday(created_at) >= julianday(?)", formatTime(from.UTC()))
		}
		if to != nil {
			add("julianday(created_at) < julianday(?)", formatTime(to.UTC()))
		}
	}
	return where, args
}

type TrafficRanking struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Requests    int64  `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
	CostMicro   int64  `json:"cost_micro"`
}
type TrafficOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type TrafficAnalytics struct {
	Items   []TrafficBucket  `json:"items"`
	Summary TrafficBucket    `json:"summary"`
	Models  []TrafficRanking `json:"models"`
	Keys    []TrafficRanking `json:"keys"`
	Options struct {
		Keys      []TrafficOption `json:"keys"`
		Models    []TrafficOption `json:"models"`
		Providers []TrafficOption `json:"providers"`
	} `json:"options"`
}

func (s *Store) TrafficAnalytics(ctx context.Context, query TrafficQuery) (TrafficAnalytics, error) {
	if s.telemetry == nil {
		return TrafficAnalytics{}, ErrTelemetryUnavailable
	}
	return s.telemetry.TrafficAnalytics(ctx, query)
}

func (t *Telemetry) TrafficAnalytics(ctx context.Context, query TrafficQuery) (TrafficAnalytics, error) {
	var result TrafficAnalytics
	items, err := t.Traffic(ctx, query)
	if err != nil {
		return result, err
	}
	result.Items = items
	db, err := t.currentDB()
	if err != nil {
		return result, err
	}
	where, args := filteredRequestLogWhere(query.APIKeyID, query.From, query.To, query.Filters)
	summary := &result.Summary
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT NULLIF(api_key_id,'')),
 COALESCE(SUM(request_tokens),0), COALESCE(SUM(response_tokens),0),
 COALESCE(SUM(cache_hit_tokens),0), COALESCE(SUM(cache_miss_tokens),0),
 COALESCE(SUM(cost_micro),0), COALESCE(SUM(CASE WHEN status_code >= 400 OR error_type <> '' THEN 1 ELSE 0 END),0),
 COALESCE(AVG(latency_ms),0) FROM request_logs `+where, args...).Scan(&summary.Requests, &summary.UniqueAPIKeys, &summary.RequestTokens, &summary.ResponseTokens, &summary.CacheHitTokens, &summary.CacheMissTokens, &summary.CostMicro, &summary.ErrorRequests, &summary.AverageLatency)
	if err != nil {
		return result, err
	}
	fillTrafficDerived(summary)
	for _, group := range []struct {
		column, name string
		target       *[]TrafficRanking
	}{
		{"public_model", "public_model", &result.Models}, {"api_key_id", "MAX(api_key_name)", &result.Keys},
	} {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s, %s, COUNT(*), COALESCE(SUM(request_tokens+response_tokens),0), COALESCE(SUM(cost_micro),0) FROM request_logs %s GROUP BY %s ORDER BY COUNT(*) DESC, %s ASC`, group.column, group.name, where, group.column, group.column), args...)
		if err != nil {
			return result, err
		}
		*group.target = []TrafficRanking{}
		for rows.Next() {
			var row TrafficRanking
			if err = rows.Scan(&row.ID, &row.Name, &row.Requests, &row.TotalTokens, &row.CostMicro); err != nil {
				rows.Close()
				return result, err
			}
			*group.target = append(*group.target, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	// Historical options include deleted keys/models and do not shrink when filters change.
	for _, group := range []struct {
		column, name string
		target       *[]TrafficOption
	}{
		{"api_key_id", "MAX(api_key_name)", &result.Options.Keys}, {"public_model", "public_model", &result.Options.Models}, {"provider", "provider", &result.Options.Providers},
	} {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s,%s FROM request_logs GROUP BY %s ORDER BY %s`, group.column, group.name, group.column, group.column))
		if err != nil {
			return result, err
		}
		*group.target = []TrafficOption{}
		for rows.Next() {
			var row TrafficOption
			if err = rows.Scan(&row.ID, &row.Name); err != nil {
				rows.Close()
				return result, err
			}
			*group.target = append(*group.target, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
