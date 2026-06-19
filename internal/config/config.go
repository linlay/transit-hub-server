package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Env struct {
	Addr                    string
	DBPath                  string
	ConfigDir               string
	IssuerConfigPath        string
	AdminToken              string
	AdminUsername           string
	AdminPassword           string
	AdminSessionTTL         time.Duration
	CORSAllowedOrigins      []string
	CookieSecure            bool
	SessionActiveWindow     time.Duration
	LogLevel                string
	UpstreamTimeout         time.Duration
	CircuitFailureThreshold int
	CircuitCooldown         time.Duration
	Currency                string
	RateLimitTimezone       string
}

type ProviderConfig struct {
	Name        string            `yaml:"name" json:"name"`
	Protocol    string            `yaml:"protocol" json:"protocol"`
	BaseURL     string            `yaml:"base_url" json:"base_url"`
	DefaultPool string            `yaml:"default_pool" json:"default_pool"`
	Headers     map[string]string `yaml:"headers" json:"headers,omitempty"`
	Endpoints   map[string]string `yaml:"endpoints" json:"endpoints,omitempty"`
	Models      []ModelConfig     `yaml:"models" json:"models"`
	Pools       []PoolConfig      `yaml:"pools" json:"pools"`
}

type ModelConfig struct {
	Public      string `yaml:"public" json:"public"`
	Upstream    string `yaml:"upstream" json:"upstream"`
	Pool        string `yaml:"pool" json:"pool,omitempty"`
	OwnedBy     string `yaml:"owned_by" json:"owned_by,omitempty"`
	DisplayName string `yaml:"display_name" json:"display_name,omitempty"`
	CreatedAt   string `yaml:"created_at" json:"created_at,omitempty"`
}

type PoolConfig struct {
	Name     string          `yaml:"name" json:"name"`
	Accounts []AccountConfig `yaml:"accounts" json:"accounts"`
}

type AccountConfig struct {
	Name       string            `yaml:"name" json:"name"`
	APIKey     string            `yaml:"api_key" json:"-"`
	Weight     int               `yaml:"weight" json:"weight"`
	Headers    map[string]string `yaml:"headers" json:"headers,omitempty"`
	AuthHeader string            `yaml:"auth_header" json:"auth_header,omitempty"`
	AuthScheme string            `yaml:"auth_scheme" json:"auth_scheme,omitempty"`
}

type IssuerConfig struct {
	PrivateKeyPath            string
	PublicKeyPath             string
	Issuer                    string
	Audience                  string
	DefaultJWTTTL             time.Duration
	DefaultAPIKeyRequestQuota int64
	DefaultAPIKeyTokenQuota   int64
}

type issuerConfigFile struct {
	PrivateKeyPath            string `yaml:"private_key_path"`
	PublicKeyPath             string `yaml:"public_key_path"`
	Issuer                    string `yaml:"issuer"`
	Audience                  string `yaml:"audience"`
	DefaultJWTTTL             string `yaml:"default_jwt_ttl"`
	DefaultAPIKeyRequestQuota int64  `yaml:"default_api_key_request_quota"`
	DefaultAPIKeyTokenQuota   int64  `yaml:"default_api_key_token_quota"`
}

func LoadEnv() (Env, error) {
	_ = godotenv.Load()

	configDir := getEnv("CONFIG_DIR", "configs")
	env := Env{
		Addr:                    getEnv("ADDR", ":8080"),
		DBPath:                  getEnv("DB_PATH", "data/transit-hub.db"),
		ConfigDir:               configDir,
		IssuerConfigPath:        getEnv("ISSUER_CONFIG_PATH", filepath.Join(configDir, "issuer", "config.yaml")),
		AdminToken:              os.Getenv("ADMIN_TOKEN"),
		AdminUsername:           getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:           os.Getenv("ADMIN_PASSWORD"),
		AdminSessionTTL:         getDurationEnv("ADMIN_SESSION_TTL", 24*time.Hour),
		CORSAllowedOrigins:      getCSVEnv("CORS_ALLOWED_ORIGINS", []string{"http://localhost:5173"}),
		CookieSecure:            getBoolEnv("COOKIE_SECURE", false),
		SessionActiveWindow:     getDurationEnv("SESSION_ACTIVE_WINDOW", 5*time.Minute),
		LogLevel:                getEnv("LOG_LEVEL", "info"),
		UpstreamTimeout:         getDurationEnv("UPSTREAM_TIMEOUT", 5*time.Minute),
		CircuitFailureThreshold: getIntEnv("CIRCUIT_FAILURE_THRESHOLD", 3),
		CircuitCooldown:         getDurationEnv("CIRCUIT_COOLDOWN", 30*time.Second),
		Currency:                getEnv("CURRENCY", "CNY"),
		RateLimitTimezone:       getEnv("RATE_LIMIT_TIMEZONE", "Asia/Shanghai"),
	}
	if strings.TrimSpace(env.AdminToken) == "" {
		return Env{}, errors.New("ADMIN_TOKEN is required")
	}
	if env.CircuitFailureThreshold < 1 {
		return Env{}, errors.New("CIRCUIT_FAILURE_THRESHOLD must be >= 1")
	}
	if env.CircuitCooldown <= 0 {
		return Env{}, errors.New("CIRCUIT_COOLDOWN must be positive")
	}
	if env.AdminSessionTTL <= 0 {
		return Env{}, errors.New("ADMIN_SESSION_TTL must be positive")
	}
	if env.SessionActiveWindow <= 0 {
		return Env{}, errors.New("SESSION_ACTIVE_WINDOW must be positive")
	}
	if _, err := time.LoadLocation(env.RateLimitTimezone); err != nil {
		return Env{}, fmt.Errorf("RATE_LIMIT_TIMEZONE is invalid: %w", err)
	}
	return env, nil
}

func LoadIssuerConfig(path string) (IssuerConfig, bool, error) {
	if strings.TrimSpace(path) == "" {
		return IssuerConfig{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return IssuerConfig{}, false, nil
		}
		return IssuerConfig{}, false, fmt.Errorf("read issuer config %s: %w", path, err)
	}
	var raw issuerConfigFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return IssuerConfig{}, false, fmt.Errorf("parse issuer config %s: %w", path, err)
	}
	cfg := IssuerConfig{
		PrivateKeyPath:            resolveRelativePath(filepath.Dir(path), raw.PrivateKeyPath),
		PublicKeyPath:             resolveRelativePath(filepath.Dir(path), raw.PublicKeyPath),
		Issuer:                    strings.TrimSpace(raw.Issuer),
		Audience:                  strings.TrimSpace(raw.Audience),
		DefaultJWTTTL:             720 * time.Hour,
		DefaultAPIKeyRequestQuota: raw.DefaultAPIKeyRequestQuota,
		DefaultAPIKeyTokenQuota:   raw.DefaultAPIKeyTokenQuota,
	}
	if strings.TrimSpace(raw.DefaultJWTTTL) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(raw.DefaultJWTTTL))
		if err != nil {
			return IssuerConfig{}, false, fmt.Errorf("default_jwt_ttl is invalid: %w", err)
		}
		cfg.DefaultJWTTTL = parsed
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "transit-hub"
	}
	if cfg.Audience == "" {
		cfg.Audience = "api-key-grant"
	}
	if strings.TrimSpace(cfg.PrivateKeyPath) == "" {
		return IssuerConfig{}, false, errors.New("private_key_path is required")
	}
	if strings.TrimSpace(cfg.PublicKeyPath) == "" {
		return IssuerConfig{}, false, errors.New("public_key_path is required")
	}
	if cfg.DefaultJWTTTL <= 0 {
		return IssuerConfig{}, false, errors.New("default_jwt_ttl must be positive")
	}
	if cfg.DefaultAPIKeyRequestQuota < 0 || cfg.DefaultAPIKeyTokenQuota < 0 {
		return IssuerConfig{}, false, errors.New("default api key quotas must be >= 0")
	}
	if cfg.DefaultAPIKeyRequestQuota == 0 {
		cfg.DefaultAPIKeyRequestQuota = 500
	}
	if cfg.DefaultAPIKeyTokenQuota == 0 {
		cfg.DefaultAPIKeyTokenQuota = 2000000
	}
	return cfg, true, nil
}

func LoadProviderConfigs(dir string) ([]ProviderConfig, error) {
	providerDir := ProviderConfigDir(dir)
	matches, err := providerFiles(providerDir)
	if err != nil {
		return nil, err
	}

	configs := make([]ProviderConfig, 0, len(matches))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read provider config %s: %w", path, err)
		}
		var cfg ProviderConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse provider config %s: %w", path, err)
		}
		if err := ValidateProviderConfig(cfg); err != nil {
			return nil, fmt.Errorf("validate provider config %s: %w", path, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func ProviderConfigDir(configDir string) string {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		configDir = "configs"
	}
	if filepath.Base(filepath.Clean(configDir)) == "providers" {
		return configDir
	}
	return filepath.Join(configDir, "providers")
}

func ValidateProviderConfig(cfg ProviderConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return errors.New("name is required")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "openai", "anthropic":
	default:
		return fmt.Errorf("protocol must be openai or anthropic")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return fmt.Errorf("base_url is invalid: %w", err)
	}
	if len(cfg.Pools) == 0 {
		return errors.New("at least one pool is required")
	}
	pools := make(map[string]struct{}, len(cfg.Pools))
	for _, pool := range cfg.Pools {
		if strings.TrimSpace(pool.Name) == "" {
			return errors.New("pool name is required")
		}
		if _, exists := pools[pool.Name]; exists {
			return fmt.Errorf("duplicate pool %q", pool.Name)
		}
		if len(pool.Accounts) == 0 {
			return fmt.Errorf("pool %q requires at least one account", pool.Name)
		}
		pools[pool.Name] = struct{}{}
		for _, account := range pool.Accounts {
			if strings.TrimSpace(account.Name) == "" {
				return fmt.Errorf("pool %q has account without name", pool.Name)
			}
			if strings.TrimSpace(account.APIKey) == "" {
				return fmt.Errorf("account %q in pool %q requires api_key", account.Name, pool.Name)
			}
			if account.Weight < 0 {
				return fmt.Errorf("account %q weight must be >= 0", account.Name)
			}
		}
	}
	if cfg.DefaultPool == "" {
		cfg.DefaultPool = cfg.Pools[0].Name
	}
	if _, ok := pools[cfg.DefaultPool]; !ok {
		return fmt.Errorf("default_pool %q does not exist", cfg.DefaultPool)
	}
	if len(cfg.Models) == 0 {
		return errors.New("at least one model mapping is required")
	}
	seenModels := map[string]struct{}{}
	for _, model := range cfg.Models {
		if strings.TrimSpace(model.Public) == "" {
			return errors.New("model public is required")
		}
		if strings.TrimSpace(model.Upstream) == "" {
			return fmt.Errorf("model %q upstream is required", model.Public)
		}
		if _, exists := seenModels[model.Public]; exists {
			return fmt.Errorf("duplicate model mapping %q", model.Public)
		}
		seenModels[model.Public] = struct{}{}
		pool := model.Pool
		if pool == "" {
			pool = cfg.DefaultPool
		}
		if _, ok := pools[pool]; !ok {
			return fmt.Errorf("model %q references missing pool %q", model.Public, pool)
		}
	}
	return nil
}

func providerFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config dir %s: %w", dir, err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
			continue
		}
		if strings.Contains(lower, ".example.") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getBoolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func getCSVEnv(key string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return fallback
	}
	return items
}

func resolveRelativePath(baseDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}
