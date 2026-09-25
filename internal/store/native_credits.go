package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// This is a storage migration marker, not a public API version.
const NativeCreditsMigration = "native_credits"

var ErrLegacyBilling = errors.New("legacy billing database: stop the server and run scripts/migrate_native_credits.py before starting native Credits billing")

// Check before any schema changes. An old database must never be opened with
// zero-filled new columns or mistaken for micro-Credits during degraded startup.
func requireNativeCredits(db *sql.DB) error {
	rows, err := db.Query(`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		if strings.Contains(column, "cost_micro") || column == "cost_quota_micro" || column == "cost_remaining_micro" || (table == "model_prices" && column == "currency") {
			return fmt.Errorf("%w (%s.%s)", ErrLegacyBilling, table, column)
		}
	}
	return rows.Err()
}

func markNativeCredits(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
 INSERT OR IGNORE INTO schema_migrations(name, applied_at) VALUES ('native_credits', strftime('%Y-%m-%dT%H:%M:%SZ','now'))`)
	return err
}
