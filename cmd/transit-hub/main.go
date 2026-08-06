package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/gateway"
	"github.com/linlay/transit-hub/internal/issuer"
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/providerquota"
	"github.com/linlay/transit-hub/internal/store"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	env, err := config.LoadEnv()
	if err != nil {
		logger.Fatalf("load env: %v", err)
	}

	db, err := store.OpenControl(env.ControlDBPath)
	store.DefaultCurrency = env.Currency
	if err != nil {
		logger.Fatalf("open control store: %v", err)
	}
	defer db.Close()

	rateLimitLocation, err := time.LoadLocation(env.RateLimitTimezone)
	if err != nil {
		logger.Fatalf("load rate limit timezone: %v", err)
	}
	usageManager, usageErr := store.NewUsageManager(env.UsageDBPath, rateLimitLocation)
	if usageErr != nil {
		logger.Printf("usage database unavailable; continuing with in-memory counters: %v", usageErr)
	}
	if keys, listErr := db.ListAPIKeys(context.Background()); listErr != nil {
		logger.Printf("load legacy usage totals failed: %v", listErr)
	} else {
		usageManager.Bootstrap(keys)
	}
	telemetry, telemetryErr := store.NewTelemetry(env.TelemetryDBPath, env.TelemetryRetention)
	if telemetryErr != nil {
		logger.Printf("telemetry database unavailable; request logging is degraded: %v", telemetryErr)
	}
	db.AttachRuntime(usageManager, telemetry)
	if env.AdminPassword != "" {
		user, created, err := db.EnsureAdminUser(context.Background(), env.AdminUsername, env.AdminPassword)
		if err != nil {
			logger.Fatalf("bootstrap admin user: %v", err)
		}
		if created {
			logger.Printf("created bootstrap admin user %q", user.Username)
		}
	} else if count, err := db.AdminUserCount(context.Background()); err == nil && count == 0 {
		logger.Printf("no admin users configured; set ADMIN_USERNAME and ADMIN_PASSWORD, or use ADMIN_TOKEN to create one")
	}

	providerConfigs, err := config.LoadProviderConfigs(env.ConfigDir)
	if err != nil {
		logger.Fatalf("load provider configs: %v", err)
	}
	registry, err := provider.NewRegistry(providerConfigs, provider.CircuitOptions{
		FailureThreshold: env.CircuitFailureThreshold,
		Cooldown:         env.CircuitCooldown,
	})
	if err != nil {
		logger.Fatalf("build provider registry: %v", err)
	}
	if len(providerConfigs) == 0 {
		logger.Printf("no provider configs loaded from %s; copy an example config and call /admin/providers/reload", config.ProviderConfigDir(env.ConfigDir))
	}
	quotaMonitor, err := providerquota.New(providerConfigs, providerquota.Options{Logger: logger})
	if err != nil {
		logger.Fatalf("build provider quota monitor: %v", err)
	}
	quotaMonitor.Start(context.Background())

	var issuerService *issuer.Service
	if issuerConfig, found, err := config.LoadIssuerConfig(env.IssuerConfigPath); err != nil {
		logger.Fatalf("load issuer config: %v", err)
	} else if found {
		issuerService, err = issuer.New(issuerConfig)
		if err != nil {
			logger.Fatalf("load jwt issuer: %v", err)
		}
		logger.Printf("jwt issuer loaded from %s", env.IssuerConfigPath)
	} else {
		logger.Printf("jwt issuer config not found at %s; /api/apply-apikey is disabled", env.IssuerConfigPath)
	}

	app := gateway.New(gateway.Options{
		Env:           env,
		Store:         db,
		Usage:         usageManager,
		Telemetry:     telemetry,
		Issuer:        issuerService,
		Registry:      registry,
		ProviderQuota: quotaMonitor,
		Logger:        logger,
	})

	server := &http.Server{
		Addr:              env.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Printf("transit-hub listening on %s", env.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
	shutdownCancel()

	quotaCtx, quotaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := quotaMonitor.Close(quotaCtx); err != nil {
		logger.Printf("close provider quota monitor: %v", err)
	}
	quotaCancel()

	usageCtx, usageCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := usageManager.Close(usageCtx); err != nil {
		logger.Printf("close usage manager: %v", err)
	}
	usageCancel()

	telemetryCtx, telemetryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := telemetry.Close(telemetryCtx); err != nil {
		logger.Printf("close telemetry: %v", err)
	}
	telemetryCancel()
}
