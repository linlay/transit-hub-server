package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrKeyNotFound    = errors.New("api key not found")
	ErrKeyInactive    = errors.New("api key inactive")
	ErrKeyExpired     = errors.New("api key expired")
	ErrQuotaExhausted = errors.New("api key quota exhausted")
)

type Store struct {
	db *sql.DB
}

type APIKey struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	KeyPrefix     string     `json:"key_prefix"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	ForcedExpired bool       `json:"forced_expired"`
	RequestQuota  int64      `json:"request_quota"`
	TokenQuota    int64      `json:"token_quota"`
	UsedRequests  int64      `json:"used_requests"`
	UsedTokens    int64      `json:"used_tokens"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CreateAPIKeyParams struct {
	Name         string
	Description  string
	ExpiresAt    *time.Time
	RequestQuota int64
	TokenQuota   int64
}

type CreatedAPIKey struct {
	APIKey
	PlainText string `json:"key"`
}

type APIKeyPatch struct {
	Name          *string
	Description   *string
	Status        *string
	ExpiresAtSet  bool
	ExpiresAt     *time.Time
	ForcedExpired *bool
	RequestQuota  *int64
	TokenQuota    *int64
}

type RequestLog struct {
	APIKeyID        string
	Protocol        string
	PublicModel     string
	UpstreamModel   string
	Provider        string
	Pool            string
	Account         string
	DeviceID        string
	Source          string
	StatusCode      int
	Latency         time.Duration
	RequestTokens   int64
	ResponseTokens  int64
	CacheHitTokens  int64
	CacheMissTokens int64
	CostMicroUSD    int64
	Estimated       bool
	ErrorType       string
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = "data/transit-hub.db"
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateAPIKey(ctx context.Context, params CreateAPIKeyParams) (CreatedAPIKey, error) {
	if strings.TrimSpace(params.Name) == "" {
		params.Name = "unnamed"
	}
	if params.RequestQuota < 0 || params.TokenQuota < 0 {
		return CreatedAPIKey{}, errors.New("quotas must be >= 0")
	}

	plain, err := GeneratePlainAPIKey()
	if err != nil {
		return CreatedAPIKey{}, err
	}
	now := time.Now().UTC()
	key := APIKey{
		ID:            newID("key"),
		Name:          strings.TrimSpace(params.Name),
		Description:   strings.TrimSpace(params.Description),
		KeyPrefix:     keyPrefix(plain),
		Status:        "active",
		ExpiresAt:     params.ExpiresAt,
		ForcedExpired: false,
		RequestQuota:  params.RequestQuota,
		TokenQuota:    params.TokenQuota,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO api_keys (
			id, key_hash, key_prefix, name, description, status, expires_at, forced_expired,
			request_quota, token_quota, used_requests, used_tokens, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
	`, key.ID, HashKey(plain), key.KeyPrefix, key.Name, key.Description, key.Status, nullableTime(key.ExpiresAt), boolInt(key.ForcedExpired), key.RequestQuota, key.TokenQuota, formatTime(key.CreatedAt), formatTime(key.UpdatedAt))
	if err != nil {
		return CreatedAPIKey{}, err
	}
	return CreatedAPIKey{APIKey: key, PlainText: plain}, nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, key_prefix, status, expires_at, forced_expired, request_quota, token_quota,
		       used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
		FROM api_keys
		WHERE deleted_at IS NULL
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, key_prefix, status, expires_at, forced_expired, request_quota, token_quota,
		       used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
		FROM api_keys
		WHERE id = ?
	`, id)
	return scanAPIKey(row)
}

func (s *Store) FindAPIKeyByPlainText(ctx context.Context, plain string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, key_prefix, status, expires_at, forced_expired, request_quota, token_quota,
		       used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
		FROM api_keys
		WHERE key_hash = ? AND deleted_at IS NULL
	`, HashKey(plain))
	key, err := scanAPIKey(row)
	if errors.Is(err, ErrNotFound) {
		return APIKey{}, ErrKeyNotFound
	}
	return key, err
}

func (s *Store) UpdateAPIKey(ctx context.Context, id string, patch APIKeyPatch) (APIKey, error) {
	key, err := s.GetAPIKey(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return APIKey{}, errors.New("name cannot be empty")
		}
		key.Name = name
	}
	if patch.Description != nil {
		key.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*patch.Status))
		if status != "active" && status != "disabled" {
			return APIKey{}, errors.New("status must be active or disabled")
		}
		key.Status = status
	}
	if patch.ExpiresAtSet {
		key.ExpiresAt = patch.ExpiresAt
	}
	if patch.ForcedExpired != nil {
		key.ForcedExpired = *patch.ForcedExpired
	}
	if patch.RequestQuota != nil {
		if *patch.RequestQuota < 0 {
			return APIKey{}, errors.New("request_quota must be >= 0")
		}
		key.RequestQuota = *patch.RequestQuota
	}
	if patch.TokenQuota != nil {
		if *patch.TokenQuota < 0 {
			return APIKey{}, errors.New("token_quota must be >= 0")
		}
		key.TokenQuota = *patch.TokenQuota
	}
	key.UpdatedAt = time.Now().UTC()

	_, err = s.db.ExecContext(ctx, `
		UPDATE api_keys
		SET name = ?, description = ?, status = ?, expires_at = ?, forced_expired = ?,
		    request_quota = ?, token_quota = ?, updated_at = ?
		WHERE id = ?
	`, key.Name, key.Description, key.Status, nullableTime(key.ExpiresAt), boolInt(key.ForcedExpired), key.RequestQuota, key.TokenQuota, formatTime(key.UpdatedAt), key.ID)
	if err != nil {
		return APIKey{}, err
	}
	return key, nil
}

func ValidateUsableKey(key APIKey, now time.Time) error {
	if key.Status != "active" || key.ForcedExpired {
		return ErrKeyInactive
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return ErrKeyExpired
	}
	if key.RequestQuota > 0 && key.UsedRequests >= key.RequestQuota {
		return ErrQuotaExhausted
	}
	if key.TokenQuota > 0 && key.UsedTokens >= key.TokenQuota {
		return ErrQuotaExhausted
	}
	return nil
}

func (s *Store) AddUsageAndLog(ctx context.Context, keyID string, log RequestLog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	tokenDelta := log.RequestTokens + log.ResponseTokens
	now := time.Now().UTC()
	deviceID := sanitizeSessionValue(log.DeviceID)
	source := ""
	if deviceID != "" {
		source = sanitizeSessionValue(defaultSource(log.Source))
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE api_keys
		SET used_requests = used_requests + 1,
		    used_tokens = used_tokens + ?,
		    last_used_at = ?,
		    updated_at = ?
		WHERE id = ?
	`, tokenDelta, formatTime(now), formatTime(now), keyID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			device_id, source, status_code, latency_ms, request_tokens, response_tokens,
			cache_hit_tokens, cache_miss_tokens, cost_microusd, estimated, error_type, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, keyID, log.Protocol, log.PublicModel, log.UpstreamModel, log.Provider, log.Pool, log.Account,
		deviceID, source, log.StatusCode, log.Latency.Milliseconds(), log.RequestTokens, log.ResponseTokens,
		log.CacheHitTokens, log.CacheMissTokens, log.CostMicroUSD, boolInt(log.Estimated), log.ErrorType, formatTime(now))
	if err != nil {
		return err
	}
	if deviceID != "" {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api_key_sessions (
				api_key_id, device_id, source, first_seen_at, last_seen_at,
				last_status_code, request_count, token_count
			) VALUES (?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(api_key_id, device_id, source) DO UPDATE SET
				last_seen_at = excluded.last_seen_at,
				last_status_code = excluded.last_status_code,
				request_count = request_count + 1,
				token_count = token_count + excluded.token_count
		`, keyID, deviceID, source, formatTime(now), formatTime(now), log.StatusCode, tokenDelta)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListRouteOverrides(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT public_model, pool FROM route_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	overrides := map[string]string{}
	for rows.Next() {
		var model, pool string
		if err := rows.Scan(&model, &pool); err != nil {
			return nil, err
		}
		overrides[model] = pool
	}
	return overrides, rows.Err()
}

func (s *Store) GetRouteOverride(ctx context.Context, publicModel string) (string, bool, error) {
	var pool string
	err := s.db.QueryRowContext(ctx, `SELECT pool FROM route_overrides WHERE public_model = ?`, publicModel).Scan(&pool)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pool, true, nil
}

func (s *Store) SetRouteOverride(ctx context.Context, publicModel, pool string) error {
	if strings.TrimSpace(publicModel) == "" {
		return errors.New("public_model is required")
	}
	if strings.TrimSpace(pool) == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM route_overrides WHERE public_model = ?`, publicModel)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO route_overrides (public_model, pool, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(public_model) DO UPDATE SET pool = excluded.pool, updated_at = excluded.updated_at
	`, publicModel, pool, formatTime(time.Now().UTC()))
	return err
}

func GeneratePlainAPIKey() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "th_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;

		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			expires_at TEXT,
			forced_expired INTEGER NOT NULL DEFAULT 0,
			request_quota INTEGER NOT NULL DEFAULT 0,
			token_quota INTEGER NOT NULL DEFAULT 0,
			used_requests INTEGER NOT NULL DEFAULT 0,
			used_tokens INTEGER NOT NULL DEFAULT 0,
			last_used_at TEXT,
			deleted_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			api_key_id TEXT NOT NULL,
			protocol TEXT NOT NULL,
			public_model TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			provider TEXT NOT NULL,
			pool TEXT NOT NULL,
			account TEXT NOT NULL,
			device_id TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			request_tokens INTEGER NOT NULL,
			response_tokens INTEGER NOT NULL,
			cache_hit_tokens INTEGER NOT NULL DEFAULT 0,
			cache_miss_tokens INTEGER NOT NULL DEFAULT 0,
			cost_microusd INTEGER NOT NULL DEFAULT 0,
			estimated INTEGER NOT NULL DEFAULT 0,
			error_type TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(api_key_id) REFERENCES api_keys(id)
		);

		CREATE TABLE IF NOT EXISTS route_overrides (
			public_model TEXT PRIMARY KEY,
			pool TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_request_logs_api_key_created_at
			ON request_logs(api_key_id, created_at DESC);
	`)
	if err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE api_keys ADD COLUMN key_prefix TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_keys ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_keys ADD COLUMN last_used_at TEXT`,
		`ALTER TABLE api_keys ADD COLUMN deleted_at TEXT`,
		`ALTER TABLE request_logs ADD COLUMN device_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE request_logs ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE request_logs ADD COLUMN cost_microusd INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE request_logs ADD COLUMN cache_hit_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE request_logs ADD COLUMN cache_miss_tokens INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS admin_users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_login_at TEXT
		);

		CREATE TABLE IF NOT EXISTS admin_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			session_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES admin_users(id)
		);

		CREATE TABLE IF NOT EXISTS model_prices (
			id TEXT PRIMARY KEY,
			protocol TEXT NOT NULL,
			public_model TEXT NOT NULL,
			input_cost_microusd_per_1m INTEGER NOT NULL DEFAULT 0,
			input_cache_hit_cost_microusd_per_1m INTEGER,
			output_cost_microusd_per_1m INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT 'USD',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(protocol, public_model)
		);

		CREATE TABLE IF NOT EXISTS api_key_sessions (
			api_key_id TEXT NOT NULL,
			device_id TEXT NOT NULL,
			source TEXT NOT NULL,
			first_seen_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			last_status_code INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			token_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(api_key_id, device_id, source),
			FOREIGN KEY(api_key_id) REFERENCES api_keys(id)
		);

		CREATE INDEX IF NOT EXISTS idx_api_keys_deleted_status
			ON api_keys(deleted_at, status);
		CREATE INDEX IF NOT EXISTS idx_request_logs_created_at
			ON request_logs(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_request_logs_provider_created_at
			ON request_logs(provider, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_api_key_sessions_last_seen
			ON api_key_sessions(last_seen_at DESC);
	`)
	if err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE model_prices ADD COLUMN input_cache_hit_cost_microusd_per_1m INTEGER`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}

type apiKeyScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(scanner apiKeyScanner) (APIKey, error) {
	var key APIKey
	var expiresAt, lastUsedAt, deletedAt sql.NullString
	var forcedExpired int
	var createdAt, updatedAt string
	err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.Description,
		&key.KeyPrefix,
		&key.Status,
		&expiresAt,
		&forcedExpired,
		&key.RequestQuota,
		&key.TokenQuota,
		&key.UsedRequests,
		&key.UsedTokens,
		&lastUsedAt,
		&deletedAt,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	if expiresAt.Valid {
		parsed, err := parseTime(expiresAt.String)
		if err != nil {
			return APIKey{}, err
		}
		key.ExpiresAt = &parsed
	}
	if lastUsedAt.Valid {
		parsed, err := parseTime(lastUsedAt.String)
		if err != nil {
			return APIKey{}, err
		}
		key.LastUsedAt = &parsed
	}
	if deletedAt.Valid {
		parsed, err := parseTime(deletedAt.String)
		if err != nil {
			return APIKey{}, err
		}
		key.DeletedAt = &parsed
	}
	key.ForcedExpired = forcedExpired != 0
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return APIKey{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return APIKey{}, err
	}
	key.CreatedAt = parsedCreatedAt
	key.UpdatedAt = parsedUpdatedAt
	return key, nil
}

func newID(prefix string) string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:])
}

func sqliteDSN(path string) string {
	return path
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(value.UTC())
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func negativePtr(value *int64) bool {
	return value != nil && *value < 0
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func keyPrefix(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func isDuplicateColumnError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func sanitizeSessionValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func defaultSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}
