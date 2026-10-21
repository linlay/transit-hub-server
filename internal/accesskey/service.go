// Package accesskey verifies external login access tokens.
package accesskey

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"time"
)

var ErrToken = errors.New("invalid access token")

type Config struct {
	PublicKeyPath string
	Algorithm     string
	Issuer        string
	Audience      string
	SubjectClaim  string
	RequiredScope string
}
type Identity struct {
	Issuer  string
	Subject string
}
type Service struct {
	Config Config
	public *rsa.PublicKey
}

// NewVerifier creates an offline JWT verifier without device-key issuance or encryption.
func NewVerifier(cfg Config) (*Service, error) {
	if cfg.Algorithm != "RS256" || cfg.Issuer == "" || cfg.Audience == "" || cfg.PublicKeyPath == "" {
		return nil, errors.New("access token requires RS256, public_key_path, issuer and audience")
	}
	if cfg.SubjectClaim == "" {
		cfg.SubjectClaim = "sub"
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
	return &Service{Config: cfg, public: public}, nil
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
