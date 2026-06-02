package issuer

import (
	"crypto"
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
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

var (
	ErrInvalidToken = errors.New("invalid jwt token")
	ErrExpiredToken = errors.New("jwt token expired")
)

type Service struct {
	privateKey                *rsa.PrivateKey
	publicKey                 *rsa.PublicKey
	issuer                    string
	audience                  string
	defaultJWTTTL             time.Duration
	defaultAPIKeyRequestQuota int64
	defaultAPIKeyTokenQuota   int64
}

type Claims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	JTI       string `json:"jti"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

func New(cfg config.IssuerConfig) (*Service, error) {
	privateKey, err := loadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}
	publicKey, err := loadPublicKey(cfg.PublicKeyPath)
	if err != nil {
		return nil, err
	}
	return &Service{
		privateKey:                privateKey,
		publicKey:                 publicKey,
		issuer:                    cfg.Issuer,
		audience:                  cfg.Audience,
		defaultJWTTTL:             cfg.DefaultJWTTTL,
		defaultAPIKeyRequestQuota: cfg.DefaultAPIKeyRequestQuota,
		defaultAPIKeyTokenQuota:   cfg.DefaultAPIKeyTokenQuota,
	}, nil
}

func (s *Service) DefaultJWTTTL() time.Duration {
	return s.defaultJWTTTL
}

func (s *Service) DefaultAPIKeyRequestQuota() int64 {
	return s.defaultAPIKeyRequestQuota
}

func (s *Service) DefaultAPIKeyTokenQuota() int64 {
	return s.defaultAPIKeyTokenQuota
}

func (s *Service) SignGrant(jti string, expiresAt, now time.Time) (string, error) {
	claims := Claims{
		Issuer:    s.issuer,
		Audience:  s.audience,
		JTI:       strings.TrimSpace(jti),
		ExpiresAt: expiresAt.Unix(),
		IssuedAt:  now.Unix(),
	}
	if claims.JTI == "" {
		return "", errors.New("jti is required")
	}
	headerJSON, err := json.Marshal(jwtHeader{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *Service) VerifyGrant(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	headerData, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerData, &header); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if header.Algorithm != "RS256" || header.Type != "JWT" {
		return Claims{}, ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(s.publicKey, crypto.SHA256, sum[:], signature); err != nil {
		return Claims{}, ErrInvalidToken
	}
	claimsData, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(claimsData, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.Issuer != s.issuer || claims.Audience != s.audience || strings.TrimSpace(claims.JTI) == "" {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt <= now.Unix() {
		return Claims{}, ErrExpiredToken
	}
	return claims, nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("parse private key %s: missing pem block", path)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse private key %s: expected rsa private key", path)
	}
	return key, nil
}

func loadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("parse public key %s: missing pem block", path)
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key %s: %w", path, err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("parse public key %s: expected rsa public key", path)
	}
	return key, nil
}
