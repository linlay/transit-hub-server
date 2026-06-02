package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type JWTGrant struct {
	JTI          string     `json:"jti"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	IssueQuota   int64      `json:"issue_quota"`
	IssuedCount  int64      `json:"issued_count"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	LastIssuedAt *time.Time `json:"last_issued_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type CreateJWTGrantParams struct {
	JTI         string
	Name        string
	Description string
	Status      string
	IssueQuota  int64
	ExpiresAt   *time.Time
}

type JWTGrantPatch struct {
	Name        *string
	Description *string
	Status      *string
	IssueQuota  *int64
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
	if params.IssueQuota < 0 {
		return JWTGrant{}, errors.New("issue_quota must be >= 0")
	}
	now := time.Now().UTC()
	grant := JWTGrant{
		JTI:         strings.TrimSpace(params.JTI),
		Name:        strings.TrimSpace(params.Name),
		Description: strings.TrimSpace(params.Description),
		Status:      status,
		IssueQuota:  params.IssueQuota,
		ExpiresAt:   params.ExpiresAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jwt_grants (
			jti, name, description, status, issue_quota, issued_count,
			expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)
	`, grant.JTI, grant.Name, grant.Description, grant.Status, grant.IssueQuota,
		nullableTime(grant.ExpiresAt), formatTime(grant.CreatedAt), formatTime(grant.UpdatedAt))
	if err != nil {
		return JWTGrant{}, err
	}
	return grant, nil
}

func (s *Store) ListJWTGrants(ctx context.Context) ([]JWTGrant, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT jti, name, description, status, issue_quota, issued_count,
		       expires_at, last_issued_at, created_at, updated_at
		FROM jwt_grants
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []JWTGrant{}
	for rows.Next() {
		grant, err := scanJWTGrant(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, grant)
	}
	return items, rows.Err()
}

func (s *Store) GetJWTGrant(ctx context.Context, jti string) (JWTGrant, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT jti, name, description, status, issue_quota, issued_count,
		       expires_at, last_issued_at, created_at, updated_at
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
			return JWTGrant{}, errors.New("issue_quota must be >= 0")
		}
		grant.IssueQuota = *patch.IssueQuota
	}
	grant.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE jwt_grants
		SET name = ?, description = ?, status = ?, issue_quota = ?, updated_at = ?
		WHERE jti = ?
	`, grant.Name, grant.Description, grant.Status, grant.IssueQuota, formatTime(grant.UpdatedAt), grant.JTI)
	if err != nil {
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
		       expires_at, last_issued_at, created_at, updated_at
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
	var createdAt, updatedAt string
	err := scanner.Scan(
		&grant.JTI,
		&grant.Name,
		&grant.Description,
		&grant.Status,
		&grant.IssueQuota,
		&grant.IssuedCount,
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
