package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestCreditsCostPrecisionCacheAndOverflow(t *testing.T) {
	hit, write := int64(2_000_000), int64(12_000_000)
	p := ModelPrice{InputCostMicroPer1MTokens: 10_000_000, InputCacheHitCostMicroPer1MTokens: &hit, OutputCostMicroPer1MTokens: 30_000_000, Billing: PriceBilling{Mode: "tokens", CacheWriteCostMicroPer1M: &write}}
	if got := TokenCost(p, 2000, 1000, 500, 200); got != 46400 {
		t.Fatalf("cache cost=%d", got)
	}
	if got := pricedSum(1, 499999); got != 0 {
		t.Fatalf("round=%d", got)
	}
	if got := pricedSum(1, 500000); got != 1 {
		t.Fatalf("round=%d", got)
	}
	if got := pricedSum(math.MaxInt64, math.MaxInt64); got != math.MaxInt64 {
		t.Fatal("overflow")
	}
	if got := addCost(math.MaxInt64-1, 2); got != math.MaxInt64 {
		t.Fatal("counter overflow")
	}
}

func TestCreditsUsageMigrationDoesNotBackCharge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := openSQLite(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE usage_totals(api_key_id TEXT PRIMARY KEY,used_requests INTEGER NOT NULL DEFAULT 0,used_tokens INTEGER NOT NULL DEFAULT 0,last_used_at TEXT,updated_at TEXT NOT NULL);INSERT INTO usage_totals VALUES('old',5,100,NULL,'2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	manager, err := NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Total("old"); got.UsedRequests != 5 || got.UsedCostMicro != 0 {
		t.Fatalf("migration %+v", got)
	}
	manager.Record("old", 1, 2, 2300, time.Now().UTC())
	if err := manager.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager, err = NewUsageManager(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(t.Context())
	if got := manager.Total("old"); got.UsedCostMicro != 2300 || got.UsedRequests != 6 {
		t.Fatalf("reload %+v", got)
	}
}

func TestCreditsGrantInheritanceAndQuotaPatch(t *testing.T) {
	db := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	defer db.Close()
	grant, err := db.CreateJWTGrant(t.Context(), CreateJWTGrantParams{JTI: "credits", CostQuotaMicro: 1_000_000, RateLimits: []RateLimit{{Window: "1h", CostQuotaMicro: 10_000}}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := db.IssueAPIKeyFromJWTGrant(t.Context(), grant.JTI, CreateAPIKeyParams{CostQuotaMicro: 99_000_000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if key.CostQuotaMicro != 1_000_000 || len(key.RateLimits) != 1 || key.RateLimits[0].CostQuotaMicro != 10_000 {
		t.Fatalf("inherit %+v", key)
	}
	zero := int64(0)
	updated, err := db.UpdateAPIKey(t.Context(), key.ID, APIKeyPatch{CostQuotaMicro: &zero})
	if err != nil || updated.CostQuotaMicro != 0 {
		t.Fatalf("clear %+v %v", updated, err)
	}
}

func TestCreditsMergeBackPreservesMoneyAndMetadata(t *testing.T) {
	dir := t.TempDir()
	options := SplitMigrationOptions{ControlPath: filepath.Join(dir, "control.db"), UsagePath: filepath.Join(dir, "usage.db"), TelemetryPath: filepath.Join(dir, "telemetry.db"), Now: time.Now().UTC(), Location: time.UTC, Retention: 30 * 24 * time.Hour}
	control := openTestStore(t, options.ControlPath)
	key, err := control.CreateAPIKey(t.Context(), CreateAPIKeyParams{Name: "migration", CostQuotaMicro: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	control.Close()
	manager, err := NewUsageManager(options.UsagePath, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	manager.Record(key.ID, 3, 4, 2300, options.Now)
	if err := manager.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	telemetry, err := NewTelemetry(options.TelemetryPath, options.Retention)
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(RequestLog{APIKeyID: key.ID, Protocol: "openai", PublicModel: "test", StatusCode: 200, RequestTokens: 3, ResponseTokens: 4, CostMicro: 2300, BillingStatus: "charged", PriceSnapshot: `{"currency":"CNY"}`, StartedAt: options.Now, CreatedAt: options.Now, CacheWriteTokens: 2, ImageCount: 1})
	if err := telemetry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeBackDatabases(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	legacy, err := openSQLite(options.ControlPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var used int64
	var status, snapshot string
	if err := legacy.QueryRow(`SELECT used_cost_micro FROM api_keys WHERE id=?`, key.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := legacy.QueryRow(`SELECT billing_status,price_snapshot FROM request_logs WHERE api_key_id=?`, key.ID).Scan(&status, &snapshot); err != nil {
		t.Fatal(err)
	}
	if used != 2300 || status != "charged" || snapshot != `{"currency":"CNY"}` {
		t.Fatalf("lost data %d %s %s", used, status, snapshot)
	}
	fresh, err := openSQLite(filepath.Join(dir, "fresh-usage.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := migrateUsage(fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := copyLegacyUsageTotals(t.Context(), legacy, fresh); err != nil {
		t.Fatal(err)
	}
	if err := fresh.QueryRow(`SELECT used_cost_micro FROM usage_totals WHERE api_key_id=?`, key.ID).Scan(&used); err != nil || used != 2300 {
		t.Fatalf("resplit %d %v", used, err)
	}
	freshLogs, err := openSQLite(filepath.Join(dir, "fresh-logs.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer freshLogs.Close()
	if err := migrateTelemetry(freshLogs); err != nil {
		t.Fatal(err)
	}
	if _, err := copyLegacyRequestLogs(t.Context(), legacy, freshLogs, options); err != nil {
		t.Fatal(err)
	}
	if err := freshLogs.QueryRow(`SELECT billing_status,price_snapshot FROM request_logs WHERE api_key_id=?`, key.ID).Scan(&status, &snapshot); err != nil || status != "charged" {
		t.Fatalf("resplit logs %s %v", status, err)
	}
}

func TestTokenTierBoundaryAndCache(t *testing.T) {
	hit := int64(140000)
	highHit := int64(280000)
	p := ModelPrice{InputCostMicroPer1MTokens: 1400000, OutputCostMicroPer1MTokens: 8400000, InputCacheHitCostMicroPer1MTokens: &hit, Billing: PriceBilling{Mode: "tokens", TokenTiers: []TokenPriceTier{{AboveInputTokens: 272000, InputCostMicroPer1M: 2800000, OutputCostMicroPer1M: 12600000, CacheHitCostMicroPer1M: &highHit}}}}
	if got := TokenCost(p, 272000, 1000, 1000, 0); got != 387940 {
		t.Fatalf("base boundary: %d", got)
	}
	if got := TokenCost(p, 272001, 1000, 1000, 0); got != 771683 {
		t.Fatalf("high tier: %d", got)
	}
	params := ModelPriceParams{InputCostMicroPer1MTokens: 1400000}
	if err := validateBilling(p.Billing, params); err != nil {
		t.Fatal(err)
	}
	p.Billing.TokenTiers = append(p.Billing.TokenTiers, p.Billing.TokenTiers[0])
	if err := validateBilling(p.Billing, params); err == nil {
		t.Fatal("duplicate tier accepted")
	}
}
