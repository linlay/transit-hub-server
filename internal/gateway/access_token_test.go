package gateway

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/linlay/transit-hub/internal/accesskey"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func accessTokenFixture(t *testing.T) (*accesskey.Service, func(map[string]any) string) {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicFile := filepath.Join(t.TempDir(), "public.pem")
	if err = os.WriteFile(publicFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := accesskey.NewVerifier(accesskey.Config{PublicKeyPath: publicFile, Algorithm: "RS256", Issuer: "https://login.example", Audience: "transit-hub", SubjectClaim: "user.id"})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(overrides map[string]any) string {
		claims := map[string]any{"iss": "https://login.example", "aud": "transit-hub", "user": map[string]any{"id": "user-a"}, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
		for k, v := range overrides {
			claims[k] = v
		}
		payload, _ := json.Marshal(claims)
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
		content := header + "." + base64.RawURLEncoding.EncodeToString(payload)
		sum := sha256.Sum256([]byte(content))
		sig, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return content + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	return svc, sign
}
