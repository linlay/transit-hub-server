package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestControlStoreKeepsTelemetryTablesOutOfRuntimeSchema(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	defer closeTestStore(t, store)

	for _, table := range []string{"request_logs", "api_key_sessions"} {
		exists, err := tableExists(t.Context(), store.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("control database unexpectedly contains %s", table)
		}
	}
	priceColumns := testColumns(t, store.db, "model_prices")
	for _, column := range []string{
		"input_cost_micro_per_1m",
		"input_cache_hit_cost_micro_per_1m",
		"output_cost_micro_per_1m",
	} {
		assertHasColumn(t, priceColumns, column)
	}
}

func TestMigrateLegacyModelPriceColumnsPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw := openRawTestDB(t, path)
	execTestSQL(t, raw, `
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
	price, ok, err := store.GetModelPrice(t.Context(), "openai", "legacy-model")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("legacy model price not found")
	}
	if price.InputCostMicroPer1MTokens != 1_000_000 || price.OutputCostMicroPer1MTokens != 2_000_000 {
		t.Fatalf("unexpected migrated price: %#v", price)
	}
	if price.InputCacheHitCostMicroPer1MTokens == nil || *price.InputCacheHitCostMicroPer1MTokens != 25_000 {
		t.Fatalf("unexpected migrated cache price: %#v", price.InputCacheHitCostMicroPer1MTokens)
	}
}

func TestUsageAndTelemetrySchemasAreIndependent(t *testing.T) {
	root := t.TempDir()
	usage, err := NewUsageManager(filepath.Join(root, "usage.db"), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	telemetry, err := NewTelemetry(filepath.Join(root, "telemetry.db"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := usage.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})

	usageColumns := testColumns(t, usage.db, "usage_buckets")
	for _, column := range []string{"api_key_id", "window", "window_start", "requests", "tokens", "cost_micro"} {
		assertHasColumn(t, usageColumns, column)
	}
	telemetryColumns := testColumns(t, mustTelemetryDB(t, telemetry), "request_logs")
	for _, column := range []string{"api_key_id", "api_key_name", "key_prefix", "cost_micro", "created_at"} {
		assertHasColumn(t, telemetryColumns, column)
	}
	var foreignKeys int
	if err := mustTelemetryDB(t, telemetry).QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM pragma_foreign_key_list('request_logs')
	`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 0 {
		t.Fatalf("telemetry request_logs has %d foreign keys, want 0", foreignKeys)
	}
}

func mustTelemetryDB(t *testing.T, telemetry *Telemetry) *sql.DB {
	t.Helper()
	db, err := telemetry.currentDB()
	if err != nil {
		t.Fatal(err)
	}
	return db
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

func testColumns(t *testing.T, db *sql.DB, table string) map[string]struct{} {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
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
