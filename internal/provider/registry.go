package provider

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

type CircuitOptions struct {
	FailureThreshold int
	Cooldown         time.Duration
}

type Registry struct {
	mu      sync.RWMutex
	options CircuitOptions

	providers map[string]*Provider
	routes    map[string]Route
}

type Provider struct {
	Name        string
	Protocol    string
	BaseURL     *url.URL
	DefaultPool string
	Headers     map[string]string
	Endpoints   map[string]string
	Pools       map[string]*Pool
}

type Pool struct {
	Name     string
	Accounts []*Account
}

type Account struct {
	Name       string
	APIKey     string
	Weight     int
	Headers    map[string]string
	AuthHeader string
	AuthScheme string
	Breaker    *CircuitBreaker
}

type Route struct {
	Protocol      string
	PublicModel   string
	UpstreamModel string
	ProviderName  string
	PoolName      string
	OwnedBy       string
	DisplayName   string
	CreatedAt     time.Time
	Provider      *Provider
	Pool          *Pool
}

type Snapshot struct {
	Providers []ProviderSnapshot `json:"providers"`
}

type ProviderSnapshot struct {
	Name        string            `json:"name"`
	Protocol    string            `json:"protocol"`
	BaseURL     string            `json:"base_url"`
	DefaultPool string            `json:"default_pool"`
	Headers     map[string]string `json:"headers,omitempty"`
	Endpoints   map[string]string `json:"endpoints,omitempty"`
	Models      []RouteSnapshot   `json:"models"`
	Pools       []PoolSnapshot    `json:"pools"`
}

type RouteSnapshot struct {
	Public        string `json:"public"`
	Upstream      string `json:"upstream"`
	Pool          string `json:"pool"`
	OverridePool  string `json:"override_pool,omitempty"`
	OverrideValid bool   `json:"override_valid,omitempty"`
}

type PoolSnapshot struct {
	Name     string            `json:"name"`
	Accounts []AccountSnapshot `json:"accounts"`
}

type AccountSnapshot struct {
	Name    string          `json:"name"`
	Weight  int             `json:"weight"`
	Circuit CircuitSnapshot `json:"circuit"`
}

var (
	ErrNoHealthyAccount = errors.New("no healthy upstream account")
	ErrProviderNotFound = errors.New("provider not found")
	ErrRouteNotFound    = errors.New("provider route not found")
	ErrPoolNotFound     = errors.New("pool not found")
	ErrAccountNotFound  = errors.New("account not found")
)

type ConnectivityTarget struct {
	ProviderName string
	PublicModel  string
	PoolName     string
	AccountName  string
}

func NewRegistry(configs []config.ProviderConfig, options CircuitOptions) (*Registry, error) {
	providers, routes, err := build(configs, options)
	if err != nil {
		return nil, err
	}
	return &Registry{
		options:   options,
		providers: providers,
		routes:    routes,
	}, nil
}

func (r *Registry) Replace(configs []config.ProviderConfig) error {
	providers, routes, err := build(configs, r.options)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.routes = routes
	return nil
}

func (r *Registry) Resolve(protocol, publicModel string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.routes[routeKey(protocol, publicModel)]
	return route, ok
}

func (r *Registry) ApplyPoolOverride(route Route, poolName string) (Route, bool) {
	if strings.TrimSpace(poolName) == "" {
		return route, true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[route.ProviderName]
	if !ok {
		return Route{}, false
	}
	pool, ok := provider.Pools[poolName]
	if !ok {
		return Route{}, false
	}
	route.PoolName = poolName
	route.Pool = pool
	return route, true
}

func (r *Registry) HasPoolForModel(publicModel, poolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		if route.PublicModel != publicModel {
			continue
		}
		if _, ok := route.Provider.Pools[poolName]; ok {
			return true
		}
	}
	return false
}

func (r *Registry) PublicModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	models := make([]string, 0, len(r.routes))
	for _, route := range r.routes {
		if _, exists := seen[route.PublicModel]; exists {
			continue
		}
		seen[route.PublicModel] = struct{}{}
		models = append(models, route.PublicModel)
	}
	sort.Strings(models)
	return models
}

func (r *Registry) PublicRoutes(protocol string) []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	routes := make([]Route, 0, len(r.routes))
	for _, route := range r.routes {
		if protocol != "" && route.Protocol != protocol {
			continue
		}
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].PublicModel != routes[j].PublicModel {
			return routes[i].PublicModel < routes[j].PublicModel
		}
		return routes[i].Protocol < routes[j].Protocol
	})
	return routes
}

func (r *Registry) ResolveConnectivityTarget(target ConnectivityTarget) (Route, *Account, error) {
	providerName := strings.TrimSpace(target.ProviderName)
	publicModel := strings.TrimSpace(target.PublicModel)
	poolName := strings.TrimSpace(target.PoolName)
	accountName := strings.TrimSpace(target.AccountName)

	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providers[providerName]
	if !ok {
		return Route{}, nil, ErrProviderNotFound
	}

	routes := make([]Route, 0)
	for _, route := range r.routes {
		if route.ProviderName != provider.Name {
			continue
		}
		if publicModel != "" && route.PublicModel != publicModel {
			continue
		}
		routes = append(routes, route)
	}
	if len(routes) == 0 {
		return Route{}, nil, ErrRouteNotFound
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].PublicModel != routes[j].PublicModel {
			return routes[i].PublicModel < routes[j].PublicModel
		}
		return routes[i].UpstreamModel < routes[j].UpstreamModel
	})
	route := routes[0]

	if poolName != "" {
		pool, ok := provider.Pools[poolName]
		if !ok {
			return Route{}, nil, ErrPoolNotFound
		}
		route.PoolName = poolName
		route.Pool = pool
	}
	if route.Pool == nil {
		return Route{}, nil, ErrPoolNotFound
	}

	if accountName == "" {
		if len(route.Pool.Accounts) == 0 {
			return Route{}, nil, ErrAccountNotFound
		}
		return route, route.Pool.Accounts[0], nil
	}
	for _, account := range route.Pool.Accounts {
		if account.Name == accountName {
			return route, account, nil
		}
	}
	return Route{}, nil, ErrAccountNotFound
}

func (r *Registry) Snapshot(overrides map[string]string) Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshot := Snapshot{Providers: make([]ProviderSnapshot, 0, len(r.providers))}
	providerNames := make([]string, 0, len(r.providers))
	for name := range r.providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, providerName := range providerNames {
		provider := r.providers[providerName]
		providerSnapshot := ProviderSnapshot{
			Name:        provider.Name,
			Protocol:    provider.Protocol,
			BaseURL:     provider.BaseURL.String(),
			DefaultPool: provider.DefaultPool,
			Headers:     cloneStringMap(provider.Headers),
			Endpoints:   cloneStringMap(provider.Endpoints),
			Models:      make([]RouteSnapshot, 0),
			Pools:       make([]PoolSnapshot, 0, len(provider.Pools)),
		}
		routeKeys := make([]string, 0, len(r.routes))
		for key, route := range r.routes {
			if route.ProviderName == provider.Name {
				routeKeys = append(routeKeys, key)
			}
		}
		sort.Strings(routeKeys)
		for _, key := range routeKeys {
			route := r.routes[key]
			model := RouteSnapshot{
				Public:   route.PublicModel,
				Upstream: route.UpstreamModel,
				Pool:     route.PoolName,
			}
			if override := overrides[route.PublicModel]; override != "" {
				model.OverridePool = override
				_, model.OverrideValid = provider.Pools[override]
			}
			providerSnapshot.Models = append(providerSnapshot.Models, model)
		}
		poolNames := make([]string, 0, len(provider.Pools))
		for poolName := range provider.Pools {
			poolNames = append(poolNames, poolName)
		}
		sort.Strings(poolNames)
		for _, poolName := range poolNames {
			pool := provider.Pools[poolName]
			poolSnapshot := PoolSnapshot{
				Name:     pool.Name,
				Accounts: make([]AccountSnapshot, 0, len(pool.Accounts)),
			}
			for _, account := range pool.Accounts {
				poolSnapshot.Accounts = append(poolSnapshot.Accounts, AccountSnapshot{
					Name:    account.Name,
					Weight:  account.Weight,
					Circuit: account.Breaker.Snapshot(),
				})
			}
			providerSnapshot.Pools = append(providerSnapshot.Pools, poolSnapshot)
		}
		snapshot.Providers = append(snapshot.Providers, providerSnapshot)
	}
	return snapshot
}

func (route Route) PickAccount() (*Account, error) {
	if route.Pool == nil {
		return nil, ErrNoHealthyAccount
	}
	candidates := make([]*Account, 0, len(route.Pool.Accounts))
	totalWeight := 0
	for _, account := range route.Pool.Accounts {
		if account.Weight <= 0 || !account.Breaker.Allow() {
			continue
		}
		candidates = append(candidates, account)
		totalWeight += account.Weight
	}
	if len(candidates) == 0 || totalWeight == 0 {
		return nil, ErrNoHealthyAccount
	}
	pick := rand.Intn(totalWeight)
	for _, account := range candidates {
		if pick < account.Weight {
			return account, nil
		}
		pick -= account.Weight
	}
	return candidates[len(candidates)-1], nil
}

func (account *Account) ApplyAuth(headers http.Header, protocol string) {
	authHeader := account.AuthHeader
	authScheme := account.AuthScheme
	if authHeader == "" {
		if protocol == "anthropic" {
			authHeader = "x-api-key"
		} else {
			authHeader = "Authorization"
		}
	}
	if authScheme == "" && strings.EqualFold(authHeader, "Authorization") {
		authScheme = "Bearer"
	}

	if authScheme == "" {
		headers.Set(authHeader, account.APIKey)
		return
	}
	headers.Set(authHeader, authScheme+" "+account.APIKey)
}

func (provider *Provider) EndpointPath(key, fallback string) string {
	if provider == nil {
		return fallback
	}
	if endpoint := strings.TrimSpace(provider.Endpoints[key]); endpoint != "" {
		return endpoint
	}
	return fallback
}

func build(configs []config.ProviderConfig, options CircuitOptions) (map[string]*Provider, map[string]Route, error) {
	providers := make(map[string]*Provider, len(configs))
	routes := make(map[string]Route)

	for _, cfg := range configs {
		if err := config.ValidateProviderConfig(cfg); err != nil {
			return nil, nil, err
		}
		if _, exists := providers[cfg.Name]; exists {
			return nil, nil, fmt.Errorf("duplicate provider %q", cfg.Name)
		}
		baseURL, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return nil, nil, fmt.Errorf("provider %q base_url: %w", cfg.Name, err)
		}
		if baseURL.Scheme == "" || baseURL.Host == "" {
			return nil, nil, fmt.Errorf("provider %q base_url must include scheme and host", cfg.Name)
		}

		defaultPool := cfg.DefaultPool
		if defaultPool == "" && len(cfg.Pools) > 0 {
			defaultPool = cfg.Pools[0].Name
		}
		provider := &Provider{
			Name:        cfg.Name,
			Protocol:    strings.ToLower(cfg.Protocol),
			BaseURL:     baseURL,
			DefaultPool: defaultPool,
			Headers:     cloneStringMap(cfg.Headers),
			Endpoints:   cloneStringMap(cfg.Endpoints),
			Pools:       make(map[string]*Pool, len(cfg.Pools)),
		}
		for _, poolConfig := range cfg.Pools {
			pool := &Pool{
				Name:     poolConfig.Name,
				Accounts: make([]*Account, 0, len(poolConfig.Accounts)),
			}
			for _, accountConfig := range poolConfig.Accounts {
				weight := accountConfig.Weight
				if weight == 0 {
					weight = 1
				}
				pool.Accounts = append(pool.Accounts, &Account{
					Name:       accountConfig.Name,
					APIKey:     accountConfig.APIKey,
					Weight:     weight,
					Headers:    cloneStringMap(accountConfig.Headers),
					AuthHeader: accountConfig.AuthHeader,
					AuthScheme: accountConfig.AuthScheme,
					Breaker:    NewCircuitBreaker(options.FailureThreshold, options.Cooldown),
				})
			}
			provider.Pools[pool.Name] = pool
		}
		providers[provider.Name] = provider

		for _, model := range cfg.Models {
			poolName := model.Pool
			if poolName == "" {
				poolName = provider.DefaultPool
			}
			pool := provider.Pools[poolName]
			ownedBy, displayName, createdAt, err := modelMetadata(provider.Name, model)
			if err != nil {
				return nil, nil, err
			}
			key := routeKey(provider.Protocol, model.Public)
			if _, exists := routes[key]; exists {
				return nil, nil, fmt.Errorf("duplicate route for protocol %q model %q", provider.Protocol, model.Public)
			}
			routes[key] = Route{
				Protocol:      provider.Protocol,
				PublicModel:   model.Public,
				UpstreamModel: model.Upstream,
				ProviderName:  provider.Name,
				PoolName:      poolName,
				OwnedBy:       ownedBy,
				DisplayName:   displayName,
				CreatedAt:     createdAt,
				Provider:      provider,
				Pool:          pool,
			}
		}
	}

	return providers, routes, nil
}

func modelMetadata(providerName string, model config.ModelConfig) (string, string, time.Time, error) {
	ownedBy := strings.TrimSpace(model.OwnedBy)
	if ownedBy == "" {
		ownedBy = providerName
	}
	displayName := strings.TrimSpace(model.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(model.Public)
	}
	createdAt := time.Unix(0, 0).UTC()
	if raw := strings.TrimSpace(model.CreatedAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", "", time.Time{}, fmt.Errorf("model %q created_at is invalid: %w", model.Public, err)
		}
		createdAt = parsed.UTC()
	}
	return ownedBy, displayName, createdAt, nil
}

func routeKey(protocol, publicModel string) string {
	return strings.ToLower(protocol) + "\x00" + publicModel
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
