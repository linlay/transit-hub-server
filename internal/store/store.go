package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrKeyNotFound         = errors.New("api key not found")
	ErrKeyInactive         = errors.New("api key inactive")
	ErrKeyExpired          = errors.New("api key expired")
	ErrQuotaExhausted      = errors.New("api key quota exhausted")
	ErrGrantNotFound       = errors.New("jwt grant not found")
	ErrGrantInactive       = errors.New("jwt grant inactive")
	ErrGrantExpired        = errors.New("jwt grant expired")
	ErrGrantQuotaExhausted = errors.New("jwt grant quota exhausted")
)

type Store struct {
	db *sql.DB
}

type APIKey struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	KeyPrefix     string     `json:"key_prefix"`
	Source        string     `json:"source"`
	IssuerJTI     string     `json:"issuer_jti,omitempty"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	ForcedExpired bool       `json:"forced_expired"`
	RequestQuota  int64      `json:"request_quota"`
	TokenQuota    int64      `json:"token_quota"`
	AllowedModels []string   `json:"allowed_models"`
	UsedRequests  int64      `json:"used_requests"`
	UsedTokens    int64      `json:"used_tokens"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CreateAPIKeyParams struct {
	Name          string
	Description   string
	Prefix        string
	Source        string
	IssuerJTI     string
	ExpiresAt     *time.Time
	RequestQuota  int64
	TokenQuota    int64
	AllowedModels []string
}

type CreatedAPIKey struct {
	APIKey
	PlainText string `json:"key"`
}

type APIKeyPatch struct {
	Name             *string
	Description      *string
	Status           *string
	ExpiresAtSet     bool
	ExpiresAt        *time.Time
	ForcedExpired    *bool
	RequestQuota     *int64
	TokenQuota       *int64
	AllowedModelsSet bool
	AllowedModels    []string
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
	CostMicro       int64
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
	return s.createAPIKeyInTx(ctx, nil, params)
}

func (s *Store) createAPIKeyInTx(ctx context.Context, tx *sql.Tx, params CreateAPIKeyParams) (CreatedAPIKey, error) {
	if strings.TrimSpace(params.Name) == "" {
		params.Name = "unnamed"
	}
	if strings.TrimSpace(params.Prefix) == "" {
		params.Prefix = "th"
	}
	if strings.TrimSpace(params.Source) == "" {
		params.Source = "admin"
	}
	if params.RequestQuota < 0 || params.TokenQuota < 0 {
		return CreatedAPIKey{}, errors.New("quotas must be >= 0")
	}
	source := strings.ToLower(strings.TrimSpace(params.Source))
	if source != "admin" && source != "jwt" {
		return CreatedAPIKey{}, errors.New("source must be admin or jwt")
	}
	allowedModels := NormalizeAllowedModels(params.AllowedModels)
	allowedModelsJSON, err := encodeAllowedModels(allowedModels)
	if err != nil {
		return CreatedAPIKey{}, err
	}

	plain, err := GeneratePlainAPIKey(params.Prefix)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	now := time.Now().UTC()
	key := APIKey{
		ID:            newID("key"),
		Name:          strings.TrimSpace(params.Name),
		Description:   strings.TrimSpace(params.Description),
		KeyPrefix:     keyPrefix(plain),
		Source:        source,
		IssuerJTI:     strings.TrimSpace(params.IssuerJTI),
		Status:        "active",
		ExpiresAt:     params.ExpiresAt,
		ForcedExpired: false,
		RequestQuota:  params.RequestQuota,
		TokenQuota:    params.TokenQuota,
		AllowedModels: allowedModels,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	exec := func(query string, args ...any) (sql.Result, error) {
		if tx != nil {
			return tx.ExecContext(ctx, query, args...)
		}
		return s.db.ExecContext(ctx, query, args...)
	}
	_, err = exec(`
		INSERT INTO api_keys (
			id, key_hash, key_prefix, name, description, source, issuer_jti, status, expires_at, forced_expired,
			request_quota, token_quota, allowed_models, used_requests, used_tokens, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
	`, key.ID, HashKey(plain), key.KeyPrefix, key.Name, key.Description, key.Source, key.IssuerJTI, key.Status, nullableTime(key.ExpiresAt), boolInt(key.ForcedExpired), key.RequestQuota, key.TokenQuota, allowedModelsJSON, formatTime(key.CreatedAt), formatTime(key.UpdatedAt))
	if err != nil {
		return CreatedAPIKey{}, err
	}
	return CreatedAPIKey{APIKey: key, PlainText: plain}, nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, key_prefix, source, issuer_jti, status, expires_at, forced_expired, request_quota, token_quota,
		       allowed_models, used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
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
		SELECT id, name, description, key_prefix, source, issuer_jti, status, expires_at, forced_expired, request_quota, token_quota,
		       allowed_models, used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
		FROM api_keys
		WHERE id = ?
	`, id)
	return scanAPIKey(row)
}

func (s *Store) FindAPIKeyByPlainText(ctx context.Context, plain string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, key_prefix, source, issuer_jti, status, expires_at, forced_expired, request_quota, token_quota,
		       allowed_models, used_requests, used_tokens, last_used_at, deleted_at, created_at, updated_at
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
	if patch.AllowedModelsSet {
		key.AllowedModels = NormalizeAllowedModels(patch.AllowedModels)
	}
	allowedModelsJSON, err := encodeAllowedModels(key.AllowedModels)
	if err != nil {
		return APIKey{}, err
	}
	key.UpdatedAt = time.Now().UTC()

	_, err = s.db.ExecContext(ctx, `
		UPDATE api_keys
		SET name = ?, description = ?, status = ?, expires_at = ?, forced_expired = ?,
		    request_quota = ?, token_quota = ?, allowed_models = ?, updated_at = ?
		WHERE id = ?
	`, key.Name, key.Description, key.Status, nullableTime(key.ExpiresAt), boolInt(key.ForcedExpired), key.RequestQuota, key.TokenQuota, allowedModelsJSON, formatTime(key.UpdatedAt), key.ID)
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
			cache_hit_tokens, cache_miss_tokens, cost_micro, estimated, error_type, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, keyID, log.Protocol, log.PublicModel, log.UpstreamModel, log.Provider, log.Pool, log.Account,
		deviceID, source, log.StatusCode, log.Latency.Milliseconds(), log.RequestTokens, log.ResponseTokens,
		log.CacheHitTokens, log.CacheMissTokens, log.CostMicro, boolInt(log.Estimated), log.ErrorType, formatTime(now))
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

func GeneratePlainAPIKey(prefix string) (string, error) {
	prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "_")
	if prefix == "" {
		prefix = "th"
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
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
			source TEXT NOT NULL DEFAULT 'admin',
			issuer_jti TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			expires_at TEXT,
			forced_expired INTEGER NOT NULL DEFAULT 0,
			request_quota INTEGER NOT NULL DEFAULT 0,
			token_quota INTEGER NOT NULL DEFAULT 0,
			allowed_models TEXT NOT NULL DEFAULT '[]',
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
			cost_micro INTEGER NOT NULL DEFAULT 0,
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

		CREATE TABLE IF NOT EXISTS jwt_grants (
			jti TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			issue_quota INTEGER NOT NULL DEFAULT 0,
			issued_count INTEGER NOT NULL DEFAULT 0,
			request_quota INTEGER NOT NULL DEFAULT 500,
			token_quota INTEGER NOT NULL DEFAULT 2000000,
			allowed_models TEXT NOT NULL DEFAULT '[]',
			jwt TEXT NOT NULL DEFAULT '',
			expires_at TEXT,
			last_issued_at TEXT,
			created_at TEXT NOT NULL,
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
		`ALTER TABLE api_keys ADD COLUMN source TEXT NOT NULL DEFAULT 'admin'`,
		`ALTER TABLE api_keys ADD COLUMN issuer_jti TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_keys ADD COLUMN last_used_at TEXT`,
		`ALTER TABLE api_keys ADD COLUMN deleted_at TEXT`,
		`ALTER TABLE api_keys ADD COLUMN allowed_models TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE request_logs ADD COLUMN device_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE request_logs ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE request_logs ADD COLUMN cache_hit_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE request_logs ADD COLUMN cache_miss_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jwt_grants ADD COLUMN request_quota INTEGER NOT NULL DEFAULT 500`,
		`ALTER TABLE jwt_grants ADD COLUMN token_quota INTEGER NOT NULL DEFAULT 2000000`,
		`ALTER TABLE jwt_grants ADD COLUMN allowed_models TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE jwt_grants ADD COLUMN jwt TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	if err := s.migrateMicroColumns(ctx, []microColumnMigration{
		{
			Table:      "request_logs",
			OldName:    "cost_microusd",
			NewName:    "cost_micro",
			Definition: "INTEGER NOT NULL DEFAULT 0",
		},
	}); err != nil {
		return err
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
			input_cost_micro_per_1m INTEGER NOT NULL DEFAULT 0,
			input_cache_hit_cost_micro_per_1m INTEGER,
			output_cost_micro_per_1m INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT 'CNY',
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
		CREATE INDEX IF NOT EXISTS idx_api_keys_source_issuer
			ON api_keys(source, issuer_jti);
		CREATE INDEX IF NOT EXISTS idx_jwt_grants_status
			ON jwt_grants(status, expires_at);
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
	return s.migrateMicroColumns(ctx, []microColumnMigration{
		{
			Table:      "model_prices",
			OldName:    "input_cost_microusd_per_1m",
			NewName:    "input_cost_micro_per_1m",
			Definition: "INTEGER NOT NULL DEFAULT 0",
		},
		{
			Table:      "model_prices",
			OldName:    "input_cache_hit_cost_microusd_per_1m",
			NewName:    "input_cache_hit_cost_micro_per_1m",
			Definition: "INTEGER",
		},
		{
			Table:      "model_prices",
			OldName:    "output_cost_microusd_per_1m",
			NewName:    "output_cost_micro_per_1m",
			Definition: "INTEGER NOT NULL DEFAULT 0",
		},
	})
}

type microColumnMigration struct {
	Table      string
	OldName    string
	NewName    string
	Definition string
}

func (s *Store) migrateMicroColumns(ctx context.Context, migrations []microColumnMigration) error {
	for _, migration := range migrations {
		if err := s.migrateMicroColumn(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateMicroColumn(ctx context.Context, migration microColumnMigration) error {
	columns, err := s.tableColumns(ctx, migration.Table)
	if err != nil {
		return err
	}
	_, hasOld := columns[migration.OldName]
	_, hasNew := columns[migration.NewName]
	switch {
	case hasOld && hasNew:
		_, err = s.db.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET %s = COALESCE(NULLIF(%s, 0), %s, %s)`,
			sqliteIdentifier(migration.Table),
			sqliteIdentifier(migration.NewName),
			sqliteIdentifier(migration.NewName),
			sqliteIdentifier(migration.OldName),
			sqliteIdentifier(migration.NewName),
		))
		return err
	case hasOld:
		_, err = s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s RENAME COLUMN %s TO %s`,
			sqliteIdentifier(migration.Table),
			sqliteIdentifier(migration.OldName),
			sqliteIdentifier(migration.NewName),
		))
		return err
	case !hasNew:
		_, err = s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN %s %s`,
			sqliteIdentifier(migration.Table),
			sqliteIdentifier(migration.NewName),
			migration.Definition,
		))
		return err
	default:
		return nil
	}
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, sqliteIdentifier(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]struct{}{}
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}

func sqliteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type apiKeyScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(scanner apiKeyScanner) (APIKey, error) {
	var key APIKey
	var expiresAt, lastUsedAt, deletedAt sql.NullString
	var forcedExpired int
	var allowedModels string
	var createdAt, updatedAt string
	err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.Description,
		&key.KeyPrefix,
		&key.Source,
		&key.IssuerJTI,
		&key.Status,
		&expiresAt,
		&forcedExpired,
		&key.RequestQuota,
		&key.TokenQuota,
		&allowedModels,
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
	decodedAllowedModels, err := decodeAllowedModels(allowedModels)
	if err != nil {
		return APIKey{}, err
	}
	key.AllowedModels = decodedAllowedModels
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

func NormalizeAllowedModels(models []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	sort.Strings(normalized)
	return normalized
}

func APIKeyAllowsModel(key APIKey, publicModel string) bool {
	publicModel = strings.TrimSpace(publicModel)
	if publicModel == "" {
		return false
	}
	for _, model := range key.AllowedModels {
		if model == publicModel {
			return true
		}
	}
	return false
}

func encodeAllowedModels(models []string) (string, error) {
	raw, err := json.Marshal(NormalizeAllowedModels(models))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeAllowedModels(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	var models []string
	if err := json.Unmarshal([]byte(value), &models); err != nil {
		return nil, err
	}
	return NormalizeAllowedModels(models), nil
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
