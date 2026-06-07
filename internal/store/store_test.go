package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyMicroColumnsRenamesAndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw := openRawTestDB(t, path)
	execTestSQL(t, raw, `
		CREATE TABLE request_logs (
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
			created_at TEXT NOT NULL
		);
		CREATE TABLE model_prices (
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
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			status_code, latency_ms, request_tokens, response_tokens, cache_hit_tokens,
			cache_miss_tokens, cost_microusd, created_at
		) VALUES (
			'key_1', 'openai', 'legacy-model', 'upstream-model', 'provider-a', 'default', 'acct',
			200, 123, 10, 20, 3, 7, 4321, '2026-06-06T00:00:00Z'
		);
		INSERT INTO model_prices (
			id, protocol, public_model, input_cost_microusd_per_1m,
			input_cache_hit_cost_microusd_per_1m, output_cost_microusd_per_1m,
			currency, created_at, updated_at
		) VALUES (
			'price_legacy', 'openai', 'legacy-model', 1000000, 25000, 2000000,
			'USD', '2026-06-06T00:00:00Z', '2026-06-06T00:00:00Z'
		);
	`)
	closeRawTestDB(t, raw)

	store := openTestStore(t, path)
	defer closeTestStore(t, store)

	requestLogColumns := testColumns(t, store, "request_logs")
	assertHasColumn(t, requestLogColumns, "cost_micro")
	assertMissingColumn(t, requestLogColumns, "cost_microusd")
	priceColumns := testColumns(t, store, "model_prices")
	for _, column := range []string{"input_cost_micro_per_1m", "input_cache_hit_cost_micro_per_1m", "output_cost_micro_per_1m"} {
		assertHasColumn(t, priceColumns, column)
	}
	for _, column := range []string{"input_cost_microusd_per_1m", "input_cache_hit_cost_microusd_per_1m", "output_cost_microusd_per_1m"} {
		assertMissingColumn(t, priceColumns, column)
	}

	var cost int64
	if err := store.db.QueryRowContext(t.Context(), `SELECT cost_micro FROM request_logs WHERE id = 1`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 4321 {
		t.Fatalf("cost_micro = %d, want 4321", cost)
	}
	price, ok, err := store.GetModelPrice(t.Context(), "openai", "legacy-model")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("legacy model price not found")
	}
	if price.InputCostMicroPer1MTokens != 1000000 || price.OutputCostMicroPer1MTokens != 2000000 {
		t.Fatalf("unexpected price costs: %#v", price)
	}
	if price.InputCacheHitCostMicroPer1MTokens == nil || *price.InputCacheHitCostMicroPer1MTokens != 25000 {
		t.Fatalf("unexpected cache hit cost: %#v", price.InputCacheHitCostMicroPer1MTokens)
	}
}

func TestMigrateLegacyMicroColumnsToleratesHalfFailedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "half-failed.db")
	raw := openRawTestDB(t, path)
	execTestSQL(t, raw, `
		CREATE TABLE request_logs (
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
			cost_micro INTEGER NOT NULL DEFAULT 0,
			estimated INTEGER NOT NULL DEFAULT 0,
			error_type TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE TABLE model_prices (
			id TEXT PRIMARY KEY,
			protocol TEXT NOT NULL,
			public_model TEXT NOT NULL,
			input_cost_microusd_per_1m INTEGER NOT NULL DEFAULT 0,
			input_cost_micro_per_1m INTEGER NOT NULL DEFAULT 0,
			input_cache_hit_cost_microusd_per_1m INTEGER,
			input_cache_hit_cost_micro_per_1m INTEGER,
			output_cost_microusd_per_1m INTEGER NOT NULL DEFAULT 0,
			output_cost_micro_per_1m INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT 'USD',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(protocol, public_model)
		);
		INSERT INTO request_logs (
			api_key_id, protocol, public_model, upstream_model, provider, pool, account,
			status_code, latency_ms, request_tokens, response_tokens, cost_microusd,
			cost_micro, created_at
		) VALUES
			('key_1', 'openai', 'legacy-model', 'upstream-model', 'provider-a', 'default', 'acct',
				200, 123, 10, 20, 4321, 0, '2026-06-06T00:00:00Z'),
			('key_1', 'openai', 'legacy-model', 'upstream-model', 'provider-a', 'default', 'acct',
				200, 456, 30, 40, 1111, 2222, '2026-06-06T00:00:01Z');
		INSERT INTO model_prices (
			id, protocol, public_model, input_cost_microusd_per_1m, input_cost_micro_per_1m,
			input_cache_hit_cost_microusd_per_1m, input_cache_hit_cost_micro_per_1m,
			output_cost_microusd_per_1m, output_cost_micro_per_1m,
			currency, created_at, updated_at
		) VALUES (
			'price_half', 'openai', 'half-model', 1000000, 0, 25000, NULL, 2000000, 3000000,
			'USD', '2026-06-06T00:00:00Z', '2026-06-06T00:00:00Z'
		);
	`)
	closeRawTestDB(t, raw)

	store := openTestStore(t, path)
	defer closeTestStore(t, store)

	rows, err := store.db.QueryContext(t.Context(), `SELECT id, cost_micro FROM request_logs ORDER BY id ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotCosts := map[int64]int64{}
	for rows.Next() {
		var id, cost int64
		if err := rows.Scan(&id, &cost); err != nil {
			t.Fatal(err)
		}
		gotCosts[id] = cost
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if gotCosts[1] != 4321 {
		t.Fatalf("row 1 cost_micro = %d, want 4321", gotCosts[1])
	}
	if gotCosts[2] != 2222 {
		t.Fatalf("row 2 cost_micro = %d, want 2222", gotCosts[2])
	}

	price, ok, err := store.GetModelPrice(t.Context(), "openai", "half-model")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("half migrated model price not found")
	}
	if price.InputCostMicroPer1MTokens != 1000000 {
		t.Fatalf("input cost = %d, want 1000000", price.InputCostMicroPer1MTokens)
	}
	if price.InputCacheHitCostMicroPer1MTokens == nil || *price.InputCacheHitCostMicroPer1MTokens != 25000 {
		t.Fatalf("cache hit cost = %#v, want 25000", price.InputCacheHitCostMicroPer1MTokens)
	}
	if price.OutputCostMicroPer1MTokens != 3000000 {
		t.Fatalf("output cost = %d, want 3000000", price.OutputCostMicroPer1MTokens)
	}
}

func TestMigrateFreshStoreCreatesMicroColumns(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "fresh.db"))
	defer closeTestStore(t, store)

	requestLogColumns := testColumns(t, store, "request_logs")
	assertHasColumn(t, requestLogColumns, "cost_micro")
	assertMissingColumn(t, requestLogColumns, "cost_microusd")

	priceColumns := testColumns(t, store, "model_prices")
	for _, column := range []string{"input_cost_micro_per_1m", "input_cache_hit_cost_micro_per_1m", "output_cost_micro_per_1m"} {
		assertHasColumn(t, priceColumns, column)
	}
	for _, column := range []string{"input_cost_microusd_per_1m", "input_cache_hit_cost_microusd_per_1m", "output_cost_microusd_per_1m"} {
		assertMissingColumn(t, priceColumns, column)
	}
}

func openRawTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func closeRawTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func execTestSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func closeTestStore(t *testing.T, store *Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func testColumns(t *testing.T, store *Store, table string) map[string]struct{} {
	t.Helper()
	columns, err := store.tableColumns(t.Context(), table)
	if err != nil {
		t.Fatal(err)
	}
	return columns
}

func assertHasColumn(t *testing.T, columns map[string]struct{}, column string) {
	t.Helper()
	if _, ok := columns[column]; !ok {
		t.Fatalf("missing column %q", column)
	}
}

func assertMissingColumn(t *testing.T, columns map[string]struct{}, column string) {
	t.Helper()
	if _, ok := columns[column]; ok {
		t.Fatalf("unexpected column %q", column)
	}
}
