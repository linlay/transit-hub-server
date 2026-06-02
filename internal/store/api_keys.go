package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type APIKeyListParams struct {
	Search         string
	Status         string
	Source         string
	IssuerJTI      string
	IncludeDeleted bool
	Limit          int
	Offset         int
}

type APIKeyListResult struct {
	Items  []APIKey `json:"items"`
	Total  int64    `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

func (s *Store) SearchAPIKeys(ctx context.Context, params APIKeyListParams) (APIKeyListResult, error) {
	limit := params.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	where := []string{}
	args := []any{}
	if !params.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if search := strings.TrimSpace(params.Search); search != "" {
		where = append(where, "(name LIKE ? OR description LIKE ? OR id LIKE ? OR key_prefix LIKE ?)")
		like := "%" + search + "%"
		args = append(args, like, like, like, like)
	}
	if status := strings.ToLower(strings.TrimSpace(params.Status)); status != "" && status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if source := strings.ToLower(strings.TrimSpace(params.Source)); source != "" && source != "all" {
		where = append(where, "source = ?")
		args = append(args, source)
	}
	if issuerJTI := strings.TrimSpace(params.IssuerJTI); issuerJTI != "" {
		where = append(where, "issuer_jti = ?")
		args = append(args, issuerJTI)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM api_keys %s`, whereSQL)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return APIKeyListResult{}, err
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, name, description, key_prefix, source, issuer_jti, status, expires_at, forced_expired, request_quota, token_quota,
		       allowed_models, used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
		FROM api_keys
		%s
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL), queryArgs...)
	if err != nil {
		return APIKeyListResult{}, err
	}
	defer rows.Close()

	items := []APIKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return APIKeyListResult{}, err
		}
		items = append(items, key)
	}
	if err := rows.Err(); err != nil {
		return APIKeyListResult{}, err
	}
	return APIKeyListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) (APIKey, error) {
	key, err := s.GetAPIKey(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE api_keys
		SET status = 'disabled',
		    forced_expired = 1,
		    deleted_at = COALESCE(deleted_at, ?),
		    updated_at = ?
		WHERE id = ?
	`, formatTime(now), formatTime(now), id)
	if err != nil {
		return APIKey{}, err
	}
	key.Status = "disabled"
	key.ForcedExpired = true
	if key.DeletedAt == nil {
		key.DeletedAt = &now
	}
	key.UpdatedAt = now
	return key, nil
}
