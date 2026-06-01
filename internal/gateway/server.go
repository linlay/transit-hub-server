package gateway

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
)

type Gateway struct {
	env      config.Env
	store    *store.Store
	registry *provider.Registry
	client   *http.Client
	logger   *log.Logger
}

type Options struct {
	Env      config.Env
	Store    *store.Store
	Registry *provider.Registry
	Client   *http.Client
	Logger   *log.Logger
}

func New(options Options) *Gateway {
	client := options.Client
	if client == nil {
		timeout := options.Env.UpstreamTimeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		client = &http.Client{Timeout: timeout}
	}
	logger := options.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Gateway{
		env:      options.Env,
		store:    options.Store,
		registry: options.Registry,
		client:   client,
		logger:   logger,
	}
}

func (g *Gateway) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(g.cors)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Post("/admin/auth/login", g.login)

	r.Route("/admin", func(r chi.Router) {
		r.Use(g.adminAuth)
		r.Get("/auth/me", g.me)
		r.Post("/auth/logout", g.logout)
		r.Get("/overview", g.overview)
		r.Get("/traffic", g.traffic)
		r.Get("/logs", g.requestLogs)
		r.Get("/sessions", g.sessions)
		r.Post("/api-keys", g.createAPIKey)
		r.Get("/api-keys", g.listAPIKeys)
		r.Get("/api-keys/{id}", g.getAPIKey)
		r.Patch("/api-keys/{id}", g.patchAPIKey)
		r.Delete("/api-keys/{id}", g.deleteAPIKey)
		r.Get("/api-keys/{id}/usage", g.apiKeyUsage)
		r.Get("/api-keys/{id}/logs", g.apiKeyLogs)
		r.Get("/api-keys/{id}/sessions", g.apiKeySessions)
		r.Get("/model-prices", g.listModelPrices)
		r.Post("/model-prices", g.createModelPrice)
		r.Patch("/model-prices/{id}", g.patchModelPrice)
		r.Delete("/model-prices/{id}", g.deleteModelPrice)
		r.Get("/users", g.listAdminUsers)
		r.Post("/users", g.createAdminUser)
		r.Patch("/users/{id}", g.patchAdminUser)
		r.Delete("/users/{id}", g.deleteAdminUser)
		r.Get("/providers", g.listProviders)
		r.Post("/providers/reload", g.reloadProviders)
		r.Put("/routes/{public_model}/pool", g.setRoutePool)
		r.Delete("/routes/{public_model}/pool", g.clearRoutePool)
	})

	r.Post("/v1/chat/completions", g.proxy("openai", "openai_chat_completions"))
	r.Post("/v1/messages", g.proxy("anthropic", "anthropic_messages"))
	return r
}

func (g *Gateway) cors(next http.Handler) http.Handler {
	allowed := map[string]struct{}{}
	for _, origin := range g.env.CORSAllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowed["*"]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else if _, ok := allowed[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-admin-token, x-api-key, x-device-id, x-source")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
