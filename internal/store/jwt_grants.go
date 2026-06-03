package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type JWTGrant struct {
	JTI           string     `json:"jti"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Status        string     `json:"status"`
	IssueQuota    int64      `json:"issue_quota"`
	IssuedCount   int64      `json:"issued_count"`
	RequestQuota  int64      `json:"request_quota"`
	TokenQuota    int64      `json:"token_quota"`
	AllowedModels []string   `json:"allowed_models"`
	JWT           string     `json:"jwt,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	LastIssuedAt  *time.Time `json:"last_issued_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CreateJWTGrantParams struct {
	JTI           string
	Name          string
	Description   string
	Status        string
	IssueQuota    int64
	RequestQuota  int64
	TokenQuota    int64
	AllowedModels []string
	JWT           string
	ExpiresAt     *time.Time
}

type JWTGrantPatch struct {
	Name             *string
	Description      *string
	Status           *string
	IssueQuota       *int64
	RequestQuota     *int64
	TokenQuota       *int64
	AllowedModelsSet bool
	AllowedModels    []string
}

type JWTGrantListParams struct {
	Search string
	Status string
	Limit  int
	Offset int
}

type JWTGrantListResult struct {
	Items  []JWTGrant `json:"items"`
	Total  int64      `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

func (s *Store) CreateJWTGrant(ctx context.Context, params CreateJWTGrantParams) (JWTGrant, error) {
	if strings.TrimSpace(params.JTI) == "" {
		return JWTGrant{}, errors.New("jti is required")
	}
	if strings.TrimSpace(params.Name) == "" {
		params.Name = "unnamed grant"
	}
	status := strings.ToLower(strings.TrimSpace(params.Status))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return JWTGrant{}, errors.New("status must be active or disabled")
	}
	if params.IssueQuota < 0 || params.RequestQuota < 0 || params.TokenQuota < 0 {
		return JWTGrant{}, errors.New("quotas must be >= 0")
	}
	allowedModels := NormalizeAllowedModels(params.AllowedModels)
	allowedModelsJSON, err := encodeAllowedModels(allowedModels)
	if err != nil {
		return JWTGrant{}, err
	}
	now := time.Now().UTC()
	grant := JWTGrant{
		JTI:           strings.TrimSpace(params.JTI),
		Name:          strings.TrimSpace(params.Name),
		Description:   strings.TrimSpace(params.Description),
		Status:        status,
		IssueQuota:    params.IssueQuota,
		RequestQuota:  params.RequestQuota,
		TokenQuota:    params.TokenQuota,
		AllowedModels: allowedModels,
		JWT:           strings.TrimSpace(params.JWT),
		ExpiresAt:     params.ExpiresAt,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jwt_grants (
			jti, name, description, status, issue_quota, issued_count,
			request_quota, token_quota, allowed_models, jwt, expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)
	`, grant.JTI, grant.Name, grant.Description, grant.Status, grant.IssueQuota, grant.RequestQuota, grant.TokenQuota, allowedModelsJSON, grant.JWT,
		nullableTime(grant.ExpiresAt), formatTime(grant.CreatedAt), formatTime(grant.UpdatedAt))
	if err != nil {
		return JWTGrant{}, err
	}
	return grant, nil
}

func (s *Store) ListJWTGrants(ctx context.Context) ([]JWTGrant, error) {
	result, err := s.SearchJWTGrants(ctx, JWTGrantListParams{})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Store) SearchJWTGrants(ctx context.Context, params JWTGrantListParams) (JWTGrantListResult, error) {
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
	if search := strings.TrimSpace(params.Search); search != "" {
		where = append(where, "(name LIKE ? OR description LIKE ? OR jti LIKE ?)")
		like := "%" + search + "%"
		args = append(args, like, like, like)
	}
	if status := strings.ToLower(strings.TrimSpace(params.Status)); status != "" && status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM jwt_grants %s`, whereSQL)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return JWTGrantListResult{}, err
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT jti, name, description, status, issue_quota, issued_count,
		       request_quota, token_quota, allowed_models, jwt, expires_at, last_issued_at, created_at, updated_at
		FROM jwt_grants
		%s
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL), queryArgs...)
	if err != nil {
		return JWTGrantListResult{}, err
	}
	defer rows.Close()

	items := []JWTGrant{}
	for rows.Next() {
		grant, err := scanJWTGrant(rows)
		if err != nil {
			return JWTGrantListResult{}, err
		}
		items = append(items, grant)
	}
	if err := rows.Err(); err != nil {
		return JWTGrantListResult{}, err
	}
	return JWTGrantListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *Store) GetJWTGrant(ctx context.Context, jti string) (JWTGrant, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT jti, name, description, status, issue_quota, issued_count,
		       request_quota, token_quota, allowed_models, jwt, expires_at, last_issued_at, created_at, updated_at
		FROM jwt_grants
		WHERE jti = ?
	`, strings.TrimSpace(jti))
	return scanJWTGrant(row)
}

func (s *Store) UpdateJWTGrant(ctx context.Context, jti string, patch JWTGrantPatch) (JWTGrant, error) {
	grant, err := s.GetJWTGrant(ctx, jti)
	if err != nil {
		return JWTGrant{}, err
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return JWTGrant{}, errors.New("name cannot be empty")
		}
		grant.Name = name
	}
	if patch.Description != nil {
		grant.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*patch.Status))
		if status != "active" && status != "disabled" {
			return JWTGrant{}, errors.New("status must be active or disabled")
		}
		grant.Status = status
	}
	if patch.IssueQuota != nil {
		if *patch.IssueQuota < 0 {
			return JWTGrant{}, errors.New("quotas must be >= 0")
		}
		grant.IssueQuota = *patch.IssueQuota
	}
	if patch.RequestQuota != nil {
		if *patch.RequestQuota < 0 {
			return JWTGrant{}, errors.New("quotas must be >= 0")
		}
		grant.RequestQuota = *patch.RequestQuota
	}
	if patch.TokenQuota != nil {
		if *patch.TokenQuota < 0 {
			return JWTGrant{}, errors.New("quotas must be >= 0")
		}
		grant.TokenQuota = *patch.TokenQuota
	}
	if patch.AllowedModelsSet {
		grant.AllowedModels = NormalizeAllowedModels(patch.AllowedModels)
	}
	allowedModelsJSON, err := encodeAllowedModels(grant.AllowedModels)
	if err != nil {
		return JWTGrant{}, err
	}
	grant.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE jwt_grants
		SET name = ?, description = ?, status = ?, issue_quota = ?, request_quota = ?, token_quota = ?, allowed_models = ?, updated_at = ?
		WHERE jti = ?
	`, grant.Name, grant.Description, grant.Status, grant.IssueQuota, grant.RequestQuota, grant.TokenQuota, allowedModelsJSON, formatTime(grant.UpdatedAt), grant.JTI)
	if err != nil {
		return JWTGrant{}, err
	}
	return grant, nil
}

func (s *Store) DeleteJWTGrant(ctx context.Context, jti string) (JWTGrant, error) {
	return s.DeleteJWTGrantWithAPIKeys(ctx, jti, false)
}

func (s *Store) DeleteJWTGrantWithAPIKeys(ctx context.Context, jti string, deleteAPIKeys bool) (JWTGrant, error) {
	grant, err := s.GetJWTGrant(ctx, jti)
	if err != nil {
		return JWTGrant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JWTGrant{}, err
	}
	defer func() { _ = tx.Rollback() }()

	trimmedJTI := strings.TrimSpace(jti)
	if deleteAPIKeys {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_keys
			SET status = 'disabled',
			    forced_expired = 1,
			    deleted_at = COALESCE(deleted_at, ?),
			    updated_at = ?
			WHERE deleted_at IS NULL AND issuer_jti = ?
		`, formatTime(now), formatTime(now), trimmedJTI); err != nil {
			return JWTGrant{}, err
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM jwt_grants WHERE jti = ?`, trimmedJTI)
	if err != nil {
		return JWTGrant{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return JWTGrant{}, err
	}
	if rowsAffected == 0 {
		return JWTGrant{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return JWTGrant{}, err
	}
	return grant, nil
}

func (s *Store) IssueAPIKeyFromJWTGrant(ctx context.Context, jti string, params CreateAPIKeyParams, now time.Time) (CreatedAPIKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	defer func() { _ = tx.Rollback() }()

	grant, err := scanJWTGrant(tx.QueryRowContext(ctx, `
		SELECT jti, name, description, status, issue_quota, issued_count,
		       request_quota, token_quota, allowed_models, jwt, expires_at, last_issued_at, created_at, updated_at
		FROM jwt_grants
		WHERE jti = ?
	`, strings.TrimSpace(jti)))
	if errors.Is(err, ErrNotFound) {
		return CreatedAPIKey{}, ErrGrantNotFound
	}
	if err != nil {
		return CreatedAPIKey{}, err
	}
	if grant.Status != "active" {
		return CreatedAPIKey{}, ErrGrantInactive
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now.UTC()) {
		return CreatedAPIKey{}, ErrGrantExpired
	}
	if grant.IssueQuota > 0 && grant.IssuedCount >= grant.IssueQuota {
		return CreatedAPIKey{}, ErrGrantQuotaExhausted
	}

	if strings.TrimSpace(params.Name) == "" {
		params.Name = "desktop key"
	}
	params.Prefix = "dk"
	params.Source = "jwt"
	params.IssuerJTI = grant.JTI
	params.RequestQuota = grant.RequestQuota
	params.TokenQuota = grant.TokenQuota
	params.AllowedModels = grant.AllowedModels
	created, err := s.createAPIKeyInTx(ctx, tx, params)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE jwt_grants
		SET issued_count = issued_count + 1,
		    last_issued_at = ?,
		    updated_at = ?
		WHERE jti = ?
	`, formatTime(now.UTC()), formatTime(now.UTC()), grant.JTI)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	if err = tx.Commit(); err != nil {
		return CreatedAPIKey{}, err
	}
	return created, nil
}

func GenerateJTI() string {
	return newID("jti")
}

type jwtGrantScanner interface {
	Scan(dest ...any) error
}

func scanJWTGrant(scanner jwtGrantScanner) (JWTGrant, error) {
	var grant JWTGrant
	var expiresAt, lastIssuedAt sql.NullString
	var allowedModels string
	var createdAt, updatedAt string
	err := scanner.Scan(
		&grant.JTI,
		&grant.Name,
		&grant.Description,
		&grant.Status,
		&grant.IssueQuota,
		&grant.IssuedCount,
		&grant.RequestQuota,
		&grant.TokenQuota,
		&allowedModels,
		&grant.JWT,
		&expiresAt,
		&lastIssuedAt,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return JWTGrant{}, ErrNotFound
	}
	if err != nil {
		return JWTGrant{}, err
	}
	decodedAllowedModels, err := decodeAllowedModels(allowedModels)
	if err != nil {
		return JWTGrant{}, err
	}
	grant.AllowedModels = decodedAllowedModels
	if expiresAt.Valid {
		parsed, err := parseTime(expiresAt.String)
		if err != nil {
			return JWTGrant{}, err
		}
		grant.ExpiresAt = &parsed
	}
	if lastIssuedAt.Valid {
		parsed, err := parseTime(lastIssuedAt.String)
		if err != nil {
			return JWTGrant{}, err
		}
		grant.LastIssuedAt = &parsed
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return JWTGrant{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return JWTGrant{}, err
	}
	grant.CreatedAt = parsedCreatedAt
	grant.UpdatedAt = parsedUpdatedAt
	return grant, nil
}
