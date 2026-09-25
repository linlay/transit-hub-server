package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNativeCreditsRejectsPartiallyMigratedQuotaSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.db")
	db := openRawTestDB(t, path)
	execTestSQL(t, db, `CREATE TABLE api_keys(id TEXT, cost_quota_micro INTEGER); INSERT INTO api_keys VALUES ('key',123);`)
	closeRawTestDB(t, db)
	if _, err := OpenControl(path); !errors.Is(err, ErrLegacyBilling) {
		t.Fatalf("must not silently create a zero native quota: %v", err)
	}
	db = openRawTestDB(t, path)
	defer closeRawTestDB(t, db)
	var old int64
	if err := db.QueryRow(`SELECT cost_quota_micro FROM api_keys`).Scan(&old); err != nil || old != 123 {
		t.Fatalf("old quota must remain untouched: %d %v", old, err)
	}
}
