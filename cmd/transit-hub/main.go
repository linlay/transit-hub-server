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
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	env, err := config.LoadEnv()
	if err != nil {
		logger.Fatalf("load env: %v", err)
	}

	db, err := store.Open(env.DBPath)
	if err != nil {
		logger.Fatalf("open store: %v", err)
	}
	defer db.Close()
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
		logger.Printf("no provider configs loaded from %s; copy an example config and call /admin/providers/reload", env.ConfigDir)
	}

	app := gateway.New(gateway.Options{
		Env:      env,
		Store:    db,
		Registry: registry,
		Logger:   logger,
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}
