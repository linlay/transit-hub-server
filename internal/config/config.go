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
	Public   string `yaml:"public" json:"public"`
	Upstream string `yaml:"upstream" json:"upstream"`
	Pool     string `yaml:"pool" json:"pool,omitempty"`
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

func LoadEnv() (Env, error) {
	_ = godotenv.Load()

	env := Env{
		Addr:                    getEnv("ADDR", ":8080"),
		DBPath:                  getEnv("DB_PATH", "data/transit-hub.db"),
		ConfigDir:               getEnv("CONFIG_DIR", "configs"),
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
	return env, nil
}

func LoadProviderConfigs(dir string) ([]ProviderConfig, error) {
	matches, err := providerFiles(dir)
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
