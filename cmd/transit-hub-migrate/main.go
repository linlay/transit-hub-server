package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	if len(os.Args) != 2 {
		logger.Fatalf("usage: transit-hub-migrate <split|verify|merge-back>")
	}
	env, err := config.LoadEnv()
	if err != nil {
		logger.Fatalf("load env: %v", err)
	}
	loc, err := time.LoadLocation(env.RateLimitTimezone)
	if err != nil {
		logger.Fatalf("load rate limit timezone: %v", err)
	}
	options := store.SplitMigrationOptions{
		ControlPath:   env.ControlDBPath,
		UsagePath:     env.UsageDBPath,
		TelemetryPath: env.TelemetryDBPath,
		Retention:     env.TelemetryRetention,
		Location:      loc,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var report store.SplitMigrationReport
	switch os.Args[1] {
	case "split":
		report, err = store.SplitDatabases(ctx, options)
	case "verify":
		report, err = store.VerifySplitDatabases(ctx, options)
	case "merge-back":
		report, err = store.MergeBackDatabases(ctx, options)
	default:
		logger.Fatalf("unknown command %q; use split, verify, or merge-back", os.Args[1])
	}
	if err != nil {
		logger.Fatalf("%s failed: %v", os.Args[1], err)
	}
	raw, err := store.MarshalMigrationReport(report)
	if err != nil {
		logger.Fatalf("encode report: %v", err)
	}
	fmt.Println(string(raw))
}
