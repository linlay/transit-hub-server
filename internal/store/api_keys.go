package store

import (
	"context"
	"database/sql"
	"errors"
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

type APIKeyBatchParams struct {
	Action    string
	IDs       []string
	IssuerJTI string
}

type APIKeyBatchResult struct {
	Action  string `json:"action"`
	Matched int64  `json:"matched"`
	Updated int64  `json:"updated"`
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
		jtiList, err := s.resolveIssuerJTIs(ctx, issuerJTI)
		if err != nil {
			return APIKeyListResult{}, err
		}
		if len(jtiList) == 0 {
			return APIKeyListResult{Items: []APIKey{}, Limit: limit, Offset: offset}, nil
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(jtiList)), ",")
		where = append(where, "issuer_jti IN ("+placeholders+")")
		for _, jti := range jtiList {
			args = append(args, jti)
		}
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
		       allowed_models, rate_limits, used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
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

func (s *Store) BatchAPIKeys(ctx context.Context, params APIKeyBatchParams) (APIKeyBatchResult, error) {
	action := strings.ToLower(strings.TrimSpace(params.Action))
	if action != "delete" && action != "inactive" {
		return APIKeyBatchResult{}, fmt.Errorf("action must be delete or inactive")
	}
	where, args, err := s.apiKeyBatchWhere(ctx, params)
	if err != nil {
		return APIKeyBatchResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKeyBatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var matched int64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM api_keys WHERE deleted_at IS NULL AND %s`, where), args...).Scan(&matched); err != nil {
		return APIKeyBatchResult{}, err
	}

	now := time.Now().UTC()
	updateArgs := []any{}
	query := ""
	if action == "delete" {
		updateArgs = append(updateArgs, formatTime(now), formatTime(now))
		query = fmt.Sprintf(`
			UPDATE api_keys
			SET status = 'disabled',
			    forced_expired = 1,
			    deleted_at = COALESCE(deleted_at, ?),
			    updated_at = ?
			WHERE deleted_at IS NULL AND %s
		`, where)
	} else {
		updateArgs = append(updateArgs, formatTime(now))
		query = fmt.Sprintf(`
			UPDATE api_keys
			SET status = 'disabled',
			    updated_at = ?
			WHERE deleted_at IS NULL AND %s
		`, where)
	}
	updateArgs = append(updateArgs, args...)
	result, err := tx.ExecContext(ctx, query, updateArgs...)
	if err != nil {
		return APIKeyBatchResult{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return APIKeyBatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return APIKeyBatchResult{}, err
	}
	return APIKeyBatchResult{Action: action, Matched: matched, Updated: updated}, nil
}

func (s *Store) apiKeyBatchWhere(ctx context.Context, params APIKeyBatchParams) (string, []any, error) {
	ids := []string{}
	seen := map[string]struct{}{}
	for _, id := range params.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	issuerJTI := strings.TrimSpace(params.IssuerJTI)
	if len(ids) == 0 && issuerJTI == "" {
		return "", nil, fmt.Errorf("ids or issuer_jti is required")
	}

	clauses := []string{}
	args := []any{}
	if len(ids) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		clauses = append(clauses, "id IN ("+placeholders+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if issuerJTI != "" {
		jtiList, err := s.resolveIssuerJTIs(ctx, issuerJTI)
		if err != nil {
			return "", nil, err
		}
		if len(jtiList) == 0 {
			return "", nil, fmt.Errorf("no jwt grants match issuer_jti %q", issuerJTI)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(jtiList)), ",")
		clauses = append(clauses, "issuer_jti IN ("+placeholders+")")
		for _, jti := range jtiList {
			args = append(args, jti)
		}
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, nil
}

func (s *Store) resolveIssuerJTIs(ctx context.Context, value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	seen := map[string]struct{}{value: {}}
	jtiList := []string{value}
	like := "%" + value + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT jti FROM jwt_grants WHERE name LIKE ?`, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jti string
		if err := rows.Scan(&jti); err != nil {
			return nil, err
		}
		jti = strings.TrimSpace(jti)
		if jti == "" {
			continue
		}
		if _, ok := seen[jti]; ok {
			continue
		}
		seen[jti] = struct{}{}
		jtiList = append(jtiList, jti)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jtiList, nil
}

func (s *Store) IssuerNamesByJTI(ctx context.Context, jtis []string) (map[string]string, error) {
	if len(jtis) == 0 {
		return map[string]string{}, nil
	}
	seen := map[string]struct{}{}
	unique := []string{}
	for _, jti := range jtis {
		jti = strings.TrimSpace(jti)
		if jti == "" {
			continue
		}
		if _, ok := seen[jti]; ok {
			continue
		}
		seen[jti] = struct{}{}
		unique = append(unique, jti)
	}
	if len(unique) == 0 {
		return map[string]string{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(unique))
	for _, jti := range unique {
		args = append(args, jti)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT jti, name FROM jwt_grants WHERE jti IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var jti, name string
		if err := rows.Scan(&jti, &name); err != nil {
			return nil, err
		}
		out[strings.TrimSpace(jti)] = name
	}
	return out, rows.Err()
}

func (s *Store) IssuerNameByJTI(ctx context.Context, jti string) (string, bool, error) {
	jti = strings.TrimSpace(jti)
	if jti == "" {
		return "", false, nil
	}
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM jwt_grants WHERE jti = ?`, jti).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}
