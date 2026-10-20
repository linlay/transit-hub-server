// Package employeeauth configures direct access-token authentication for model APIs.
package employeeauth

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/linlay/transit-hub/internal/accesskey"
	"github.com/linlay/transit-hub/internal/store"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Enabled           bool              `yaml:"enabled"`
	PublicKeyPath     string            `yaml:"public_key_path"`
	Algorithm         string            `yaml:"algorithm"`
	Issuer            string            `yaml:"issuer"`
	Audience          string            `yaml:"audience"`
	SubjectClaim      string            `yaml:"subject_claim"`
	RequiredScope     string            `yaml:"required_scope"`
	AllowedModels     []string          `yaml:"allowed_models"`
	RequestQuota      int64             `yaml:"request_quota"`
	TokenQuota        int64             `yaml:"token_quota"`
	QuotaMicrocredits int64             `yaml:"quota_microcredits"`
	RateLimits        []store.RateLimit `yaml:"rate_limits"`
}

type Service struct {
	Config   Config
	Verifier *accesskey.Service
}

func Load(path string) (*Service, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err = d.Decode(&cfg); err != nil {
		return nil, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("employee auth requires a single YAML document")
	}
	if !cfg.Enabled {
		return nil, nil
	}
	if !filepath.IsAbs(cfg.PublicKeyPath) {
		cfg.PublicKeyPath = filepath.Join(filepath.Dir(path), cfg.PublicKeyPath)
	}
	return New(cfg)
}

func New(cfg Config) (*Service, error) {
	if strings.TrimSpace(cfg.SubjectClaim) == "" {
		cfg.SubjectClaim = "staffuserid"
	}
	for _, part := range strings.Split(cfg.SubjectClaim, ".") {
		if strings.TrimSpace(part) == "" || strings.TrimSpace(part) != part {
			return nil, errors.New("invalid employee subject_claim")
		}
	}
	if cfg.Algorithm == "" {
		cfg.Algorithm = "RS256"
	}
	cfg.AllowedModels = store.NormalizeAllowedModels(cfg.AllowedModels)
	if len(cfg.AllowedModels) == 0 || cfg.RequestQuota < 0 || cfg.TokenQuota < 0 || cfg.QuotaMicrocredits < 0 {
		return nil, errors.New("employee auth requires allowed_models and nonnegative quotas")
	}
	var err error
	cfg.RateLimits, err = store.NormalizeRateLimits(cfg.RateLimits)
	if err != nil {
		return nil, err
	}
	verifier, err := accesskey.NewVerifier(accesskey.Config{
		PublicKeyPath: cfg.PublicKeyPath, Algorithm: cfg.Algorithm, Issuer: cfg.Issuer,
		Audience: cfg.Audience, SubjectClaim: cfg.SubjectClaim, RequiredScope: cfg.RequiredScope,
	})
	if err != nil {
		return nil, err
	}
	return &Service{Config: cfg, Verifier: verifier}, nil
}
