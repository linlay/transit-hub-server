package gateway

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/accesskey"
	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
)

func deviceKeyFixture(t *testing.T) (*accesskey.Service, func(map[string]any) string) {
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
	t.Setenv("TEST_BIND_AES", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{41}, 32)))
	svc, err := accesskey.New(accesskey.Config{PublicKeyPath: publicFile, Algorithm: "RS256", Issuer: "https://login.example", Audience: "transit-hub", SubjectClaim: "user.id", ActiveEncryptionKey: "test", EncryptionKeyEnvs: map[string]string{"test": "TEST_BIND_AES"}, AllowedModels: []string{"public-model"}, RequestQuota: 50, TokenQuota: 5000, APIKeyTTL: "24h"})
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

func TestDeviceKeyBindingValidationRecoveryAndIsolation(t *testing.T) {
	svc, sign := deviceKeyFixture(t)
	app, db, _ := newTestGateway(t, nil)
	app.accessKeys = svc
	// Bind directly to exercise SQLite persistence without depending on provider routing fixtures.
	identity, err := svc.Verify(sign(nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := db.BindAPIKey(t.Context(), svc, identity, "desktop-a", "Desktop", time.Now().UTC())
	if err != nil || !created {
		t.Fatalf("first binding: %v %v", created, err)
	}
	if first.Source != "access_token" || first.IssuerJTI != "" || first.RequestQuota != 50 {
		t.Fatalf("wrong policy: %+v", first.APIKey)
	}
	second, created, err := db.BindAPIKey(t.Context(), svc, identity, "desktop-a", "Renamed", time.Now().UTC())
	if err != nil || created || second.PlainText != first.PlainText || second.ID != first.ID || !second.ExpiresAt.Equal(*first.ExpiresAt) {
		t.Fatalf("repeat binding changed: %v %v", created, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, _, err := db.BindAPIKey(t.Context(), svc, identity, "concurrent", "Concurrent", time.Now().UTC())
			if err != nil {
				t.Error(err)
			} else if key.PlainText == "" {
				t.Error("empty key")
			}
		}()
	}
	wg.Wait()
	keys, err := db.ListAPIKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, key := range keys {
		if key.Source == "access_token" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("concurrent duplicate keys: %d", count)
	}
	other, _, err := db.BindAPIKey(t.Context(), svc, accesskey.Identity{Issuer: identity.Issuer, Subject: "user-b"}, "desktop-a", "Other", time.Now().UTC())
	if err != nil || other.PlainText == first.PlainText {
		t.Fatalf("account isolation: %v", err)
	}
	status := "disabled"
	if _, err = db.UpdateAPIKey(t.Context(), first.ID, store.APIKeyPatch{Status: &status}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.BindAPIKey(t.Context(), svc, identity, "desktop-a", "Again", time.Now().UTC()); err != store.ErrDeviceKeyUnavailable {
		t.Fatalf("disabled key reissued: %v", err)
	}
	if _, _, err = db.BindAPIKey(t.Context(), svc, identity, "concurrent", "Expired", time.Now().Add(48*time.Hour)); err != store.ErrDeviceKeyUnavailable {
		t.Fatalf("expired key reissued: %v", err)
	}
	for name, overrides := range map[string]map[string]any{
		"wrong issuer": {"iss": "other"}, "wrong audience": {"aud": "other"}, "expired": {"exp": time.Now().Add(-time.Minute).Unix()}, "future": {"nbf": time.Now().Add(time.Hour).Unix()}, "missing subject": {"user": map[string]any{}}, "missing expiry": {"exp": nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Verify(sign(overrides), time.Now()); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	if _, err := svc.Verify(sign(map[string]any{"aud": []string{"other", "transit-hub"}}), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(sign(nil)+"tampered", time.Now()); err == nil {
		t.Fatal("invalid signature accepted")
	}
	id, encrypted, err := svc.Seal("test-secret", []byte("owner"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("test-secret")) {
		t.Fatal("plaintext persisted")
	}
	if _, err = svc.Open(id, encrypted, []byte("other")); err == nil {
		t.Fatal("AAD not enforced")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err = svc.Open(id, encrypted, []byte("owner")); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestBindAPIKeyHTTP(t *testing.T) {
	svc, sign := deviceKeyFixture(t)
	app, _, _ := newTestGateway(t, nil)
	request := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/bind-apikey", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := request(sign(nil), `{"device_id":"desktop"}`); rec.Code != 503 {
		t.Fatal(rec.Code)
	}
	app.accessKeys = svc
	if rec := request("bad", `{"device_id":"desktop"}`); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	for _, body := range []string{`{}`, `{"device_id":""}`, `{"device_id":"a","user_id":"forged"}`, `{"device_id":"a"} {}`, `{"device_id":"a","request_quota":0}`} {
		if rec := request(sign(nil), body); rec.Code != 400 {
			t.Fatalf("body %s: %d", body, rec.Code)
		}
	}
	// A missing model policy is a deployment error, not permission to issue unrestricted keys.
	if rec := request(sign(nil), `{"device_id":"desktop"}`); rec.Code != 503 {
		t.Fatalf("missing model policy: %d", rec.Code)
	}
}

func TestBindAPIKeyHTTPSuccessAndRepeat(t *testing.T) {
	svc, sign := deviceKeyFixture(t)
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	app.accessKeys = svc
	bind := func(token string, status int) createAPIKeyResponse {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/bind-apikey", bytes.NewBufferString(`{"device_id":"desktop","name":"Desktop"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("bind status %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response can be cached")
		}
		var result createAPIKeyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := bind(sign(nil), 201)
	repeat := bind(sign(map[string]any{"jti": "refreshed-token"}), 200)
	if first.Key == "" || first.Key != repeat.Key || first.ID != repeat.ID || repeat.DeviceBinding.Subject != "user-a" || repeat.DeviceBinding.DeviceID != "desktop" {
		t.Fatal("repeat response or binding mismatch")
	}
	key, err := db.FindAPIKeyByPlainText(t.Context(), first.Key)
	if err != nil || key.ID != first.ID {
		t.Fatal("bound key cannot authenticate")
	}
	req := httptest.NewRequest("GET", "/admin/api-keys/"+first.ID, nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte(first.Key)) || !bytes.Contains(rec.Body.Bytes(), []byte("device_binding")) {
		t.Fatalf("admin detail does not protect ciphertext / show binding: %s", rec.Body.String())
	}
	forced := true
	if _, err := db.UpdateAPIKey(t.Context(), first.ID, store.APIKeyPatch{ForcedExpired: &forced}); err != nil {
		t.Fatal(err)
	}
	bind(sign(nil), 409)
}

func TestBindingSurvivesRestartWithoutResettingPolicy(t *testing.T) {
	svc, _ := deviceKeyFixture(t)
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := store.OpenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := accesskey.Identity{Issuer: svc.Config.Issuer, Subject: "persistent-user"}
	first, _, err := db.BindAPIKey(t.Context(), svc, identity, "persistent-device", "Desktop", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// New service instance loads the public key and encryption key again.
	restarted, err := accesskey.New(svc.Config)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Config.RequestQuota = 999
	repeated, created, err := db.BindAPIKey(t.Context(), restarted, identity, "persistent-device", "Desktop", time.Now().UTC())
	if err != nil || created || repeated.PlainText != first.PlainText || repeated.RequestQuota != 50 {
		t.Fatalf("restart reset binding: %v", err)
	}
	missing := svc.Config
	missing.ActiveEncryptionKey = "missing"
	if _, err = accesskey.New(missing); err == nil {
		t.Fatal("missing encryption key accepted")
	}
	if _, err = accesskey.Load(filepath.Join(t.TempDir(), "absent.yaml")); err != nil {
		t.Fatal(err)
	}
	invalid := svc.Config
	invalid.Algorithm = "HS256"
	if _, err = accesskey.New(invalid); err == nil {
		t.Fatal("symmetric JWT algorithm accepted")
	}
}
