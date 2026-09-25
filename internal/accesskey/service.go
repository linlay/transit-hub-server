// Package accesskey verifies external login tokens and protects recoverable device keys.
package accesskey

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrToken = errors.New("invalid access token")

type RateLimit struct {
	Window            string `yaml:"window" json:"window"`
	RequestQuota      int64  `yaml:"request_quota" json:"request_quota"`
	TokenQuota        int64  `yaml:"token_quota" json:"token_quota"`
	QuotaMicrocredits int64  `yaml:"quota_microcredits" json:"quota_microcredits,string"`
}

type Config struct {
	QuotaMicrocredits   int64             `yaml:"quota_microcredits"`
	RateLimits          []RateLimit       `yaml:"rate_limits"`
	Enabled             bool              `yaml:"enabled"`
	PublicKeyPath       string            `yaml:"public_key_path"`
	Algorithm           string            `yaml:"algorithm"`
	Issuer              string            `yaml:"issuer"`
	Audience            string            `yaml:"audience"`
	SubjectClaim        string            `yaml:"subject_claim"`
	RequiredScope       string            `yaml:"required_scope"`
	ActiveEncryptionKey string            `yaml:"active_encryption_key"`
	EncryptionKeyEnvs   map[string]string `yaml:"encryption_key_envs"`
	AllowedModels       []string          `yaml:"allowed_models"`
	RequestQuota        int64             `yaml:"request_quota"`
	TokenQuota          int64             `yaml:"token_quota"`
	APIKeyTTL           string            `yaml:"api_key_ttl"`
}
type Identity struct {
	Issuer  string
	Subject string
}
type Service struct {
	Config Config
	public *rsa.PublicKey
	keys   map[string]cipher.AEAD
	TTL    time.Duration
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err = decoder.Decode(&cfg); err != nil {
		return nil, err
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
	if cfg.Algorithm != "RS256" || cfg.Issuer == "" || cfg.Audience == "" || cfg.PublicKeyPath == "" {
		return nil, errors.New("bind-apikey requires RS256, public_key_path, issuer and audience")
	}
	if cfg.SubjectClaim == "" {
		cfg.SubjectClaim = "sub"
	}
	if len(cfg.AllowedModels) == 0 || cfg.RequestQuota < 0 || cfg.TokenQuota < 0 || cfg.QuotaMicrocredits < 0 {
		return nil, errors.New("bind-apikey requires allowed_models and nonnegative quotas")
	}
	ttl, err := time.ParseDuration(cfg.APIKeyTTL)
	if err != nil || ttl <= 0 {
		return nil, errors.New("bind-apikey requires positive api_key_ttl")
	}
	data, err := os.ReadFile(cfg.PublicKeyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid access token public PEM")
	}
	var public *rsa.PublicKey
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		public, _ = key.(*rsa.PublicKey)
	} else {
		public, _ = x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if public == nil || public.N.BitLen() < 2048 {
		return nil, errors.New("access token public key must be RSA >= 2048 bits")
	}
	svc := &Service{Config: cfg, public: public, keys: map[string]cipher.AEAD{}, TTL: ttl}
	for id, env := range cfg.EncryptionKeyEnvs {
		raw, err := base64.StdEncoding.DecodeString(os.Getenv(env))
		if err != nil || len(raw) != 32 || id == "" {
			return nil, fmt.Errorf("invalid AES-256 key configuration for key id %q", id)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		svc.keys[id] = aead
	}
	if svc.keys[cfg.ActiveEncryptionKey] == nil {
		return nil, errors.New("active encryption key is not configured")
	}
	return svc, nil
}
func (s *Service) Verify(token string, now time.Time) (Identity, error) {
	fail := func() (Identity, error) { return Identity{}, ErrToken }
	if len(token) > 16384 {
		return fail()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fail()
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fail()
	}
	var h struct {
		Alg  string   `json:"alg"`
		Crit []string `json:"crit"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "RS256" || len(h.Crit) != 0 {
		return fail()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fail()
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(s.public, crypto.SHA256, digest[:], signature) != nil {
		return fail()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fail()
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return fail()
	}
	var issuer, subject string
	var exp int64
	if json.Unmarshal(claims["iss"], &issuer) != nil || issuer != s.Config.Issuer || json.Unmarshal(claims["exp"], &exp) != nil || exp <= now.Unix() {
		return fail()
	}
	// Configured dotted claim paths support identities nested in a user object.
	value := json.RawMessage(payload)
	for _, segment := range strings.Split(s.Config.SubjectClaim, ".") {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil {
			return fail()
		}
		value = object[segment]
	}
	if json.Unmarshal(value, &subject) != nil || strings.TrimSpace(subject) == "" || len(subject) > 512 {
		return fail()
	}
	for _, key := range []string{"nbf", "iat"} {
		if raw, ok := claims[key]; ok {
			var date int64
			if json.Unmarshal(raw, &date) != nil || date > now.Unix()+30 {
				return fail()
			}
		}
	}
	var audiences []string
	var audience string
	if json.Unmarshal(claims["aud"], &audience) == nil {
		audiences = []string{audience}
	} else if json.Unmarshal(claims["aud"], &audiences) != nil {
		return fail()
	}
	found := false
	for _, aud := range audiences {
		if aud == s.Config.Audience {
			found = true
		}
	}
	if !found {
		return fail()
	}
	if s.Config.RequiredScope != "" {
		var scope string
		if json.Unmarshal(claims["scope"], &scope) != nil {
			return fail()
		}
		found = false
		for _, item := range strings.Fields(scope) {
			if item == s.Config.RequiredScope {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	return Identity{issuer, subject}, nil
}
func (s *Service) Seal(plain string, aad []byte) (string, []byte, error) {
	id := s.Config.ActiveEncryptionKey
	aead := s.keys[id]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	return id, aead.Seal(nonce, nonce, []byte(plain), aad), nil
}
func (s *Service) Open(id string, encrypted, aad []byte) (string, error) {
	aead := s.keys[id]
	if aead == nil || len(encrypted) < aead.NonceSize() {
		return "", errors.New("device key encryption key unavailable or ciphertext invalid")
	}
	plain, err := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], aad)
	return string(plain), err
}
