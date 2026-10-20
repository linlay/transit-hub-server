package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/employeeauth"
	"github.com/linlay/transit-hub/internal/store"
)

func employeeFixture(t *testing.T) (*employeeauth.Service, func(map[string]any) string) {
	t.Helper()
	device, sign := deviceKeyFixture(t)
	svc, err := employeeauth.New(employeeauth.Config{
		PublicKeyPath: device.Config.PublicKeyPath, Issuer: device.Config.Issuer,
		Audience: device.Config.Audience, AllowedModels: []string{"public-model"}, RequestQuota: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, func(overrides map[string]any) string {
		claims := map[string]any{"staffuserid": "staff-a"}
		for k, v := range overrides {
			claims[k] = v
		}
		return sign(claims)
	}
}

func employeeRequest(app *Gateway, token, path, device string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if path == "/v1/chat/completions" {
		r = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`))
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("x-device-id", device)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}

func TestEmployeeDirectModelAccessSharesQuotaAcrossTokensAndDevices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Error("employee token was forwarded upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()
	app, db, apiKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	svc, sign := employeeFixture(t)
	app.employeeAuth = svc
	for i, device := range []string{"desktop-a", "desktop-b"} {
		token := sign(map[string]any{"jti": device, "exp": time.Now().Add(time.Duration(i+1) * time.Hour).Unix()})
		w := employeeRequest(app, token, "/v1/chat/completions", device)
		if w.Code != 200 {
			t.Fatalf("direct request: %d %s", w.Code, w.Body.String())
		}
	}
	w := employeeRequest(app, sign(nil), "/v1/chat/completions", "third-device")
	if w.Code != 429 {
		t.Fatalf("new token/device bypassed quota: %d %s", w.Code, w.Body.String())
	}
	result, err := db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee"})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("employee identities: %+v %v", result, err)
	}
	key := result.Items[0]
	if key.UsedRequests != 2 || key.UsedTokens != 14 || key.EmployeeIdentity.Subject != "staff-a" || key.EmployeeIdentity.Claim != "staffuserid" || key.KeyPrefix != "" {
		t.Fatalf("wrong identity/usage: %+v", key)
	}
	// Metadata remains readable when lifetime quota is exhausted.
	w = employeeRequest(app, sign(nil), "/api/me", "desktop-a")
	if w.Code != 200 || !strings.Contains(w.Body.String(), key.ID) {
		t.Fatalf("employee metadata: %d %s", w.Code, w.Body.String())
	}
	if w = employeeRequest(app, sign(map[string]any{"staffuserid": "staff-b"}), "/v1/chat/completions", "desktop-a"); w.Code != 200 {
		t.Fatalf("another employee shares quota: %d", w.Code)
	}
	if w = employeeRequest(app, apiKey, "/v1/chat/completions", "old-client"); w.Code != 200 {
		t.Fatalf("API key regression: %d", w.Code)
	}
	// Both admin surfaces use the same stable IDs and usage, with separate lists.
	w = employeeRequest(app, "admin", "/admin/employee-keys", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"subject":"staff-a"`) {
		t.Fatalf("employee list: %d %s", w.Code, w.Body.String())
	}
	w = employeeRequest(app, "admin", "/admin/api-keys", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"source":"employee"`) {
		t.Fatalf("employee leaked into grant list: %d %s", w.Code, w.Body.String())
	}
}

func TestEmployeeTokenValidationAndIdentityRevocation(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://unused.invalid")})
	svc, sign := employeeFixture(t)
	app.employeeAuth = svc
	for name, claims := range map[string]map[string]any{
		"missing identity": {"staffuserid": nil}, "numeric identity": {"staffuserid": 42},
		"blank identity": {"staffuserid": " "}, "wrong issuer": {"iss": "https://other.invalid"},
		"wrong audience": {"aud": "other"}, "expired": {"exp": time.Now().Add(-time.Minute).Unix()},
		"missing expiration": {"exp": nil}, "future token": {"nbf": time.Now().Add(time.Hour).Unix()},
	} {
		t.Run(name, func(t *testing.T) {
			if w := employeeRequest(app, sign(claims), "/api/me", ""); w.Code != 401 {
				t.Fatalf("invalid token accepted: %d", w.Code)
			}
		})
	}
	if w := employeeRequest(app, sign(nil)+"tampered", "/api/me", ""); w.Code != 401 {
		t.Fatal("bad signature accepted")
	}
	identities, _ := db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee"})
	if identities.Total != 0 {
		t.Fatal("unverified identities were persisted")
	}
	app.employeeAuth = nil
	if w := employeeRequest(app, sign(nil), "/api/me", ""); w.Code != 401 {
		t.Fatal("disabled feature accepted token")
	}
	app.employeeAuth = svc
	w := employeeRequest(app, sign(nil), "/api/me", "")
	if w.Code != 200 {
		t.Fatalf("valid token: %d %s", w.Code, w.Body.String())
	}
	identities, _ = db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee"})
	id := identities.Items[0].ID
	disabled := "disabled"
	if _, err := db.UpdateAPIKey(t.Context(), id, store.APIKeyPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if w = employeeRequest(app, sign(map[string]any{"jti": "renewed"}), "/api/me", "new-device"); w.Code != 401 {
		t.Fatal("new token bypassed disable")
	}
	if _, err := db.DeleteAPIKey(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if w = employeeRequest(app, sign(nil), "/api/me", ""); w.Code != 401 {
		t.Fatal("deleted employee was recreated")
	}
	all, _ := db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee", IncludeDeleted: true})
	if all.Total != 1 {
		t.Fatal("revocation reset identity")
	}
}

func TestEmployeeConfigurationAndCustomClaim(t *testing.T) {
	svc, sign := employeeFixture(t)
	cfg := svc.Config
	cfg.SubjectClaim = "user.id"
	cfg.RequiredScope = "models:call"
	custom, err := employeeauth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := custom.Verifier.Verify(sign(map[string]any{"scope": "profile models:call"}), time.Now())
	if err != nil || identity.Subject != "user-a" {
		t.Fatalf("custom nested claim: %+v %v", identity, err)
	}
	if _, err = custom.Verifier.Verify(sign(nil), time.Now()); err == nil {
		t.Fatal("missing scope accepted")
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	if disabled, err := employeeauth.Load(file); err != nil || disabled != nil {
		t.Fatalf("missing config: %v", err)
	}
	content := "enabled: true\npublic_key_path: " + cfg.PublicKeyPath + "\nissuer: https://login.example\naudience: transit-hub\nallowed_models: [public-model]\nrate_limits: [{window: 1h, request_quota: 3, quota_microcredits: 1000}]\n"
	if err = os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := employeeauth.Load(file)
	if err != nil || loaded.Config.SubjectClaim != "staffuserid" || loaded.Config.RateLimits[0].RequestQuota != 3 || loaded.Config.RateLimits[0].QuotaMicrocredits != 1000 {
		t.Fatalf("config defaults/limits: %+v %v", loaded, err)
	}
	if err = os.WriteFile(file, []byte(content+"typo: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = employeeauth.Load(file); err == nil {
		t.Fatal("unknown config accepted")
	}
}

func TestEmployeeIdentityConcurrentFirstUseAndNamespaceIsolation(t *testing.T) {
	_, db, _ := newTestGateway(t, nil)
	identity := store.EmployeeIdentity{Issuer: "issuer-a", Claim: "staffuserid", Subject: "staff-a"}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		wg.Go(func() {
			key, err := db.ResolveEmployeeKey(t.Context(), identity, store.CreateAPIKeyParams{RequestQuota: 10, AllowedModels: []string{"public-model"}})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- key.ID
		})
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate employee identities")
		}
	}
	if first == "" {
		t.Fatal("no identity created")
	}
	for _, other := range []store.EmployeeIdentity{{Issuer: "issuer-b", Claim: identity.Claim, Subject: identity.Subject}, {Issuer: identity.Issuer, Claim: "sub", Subject: identity.Subject}} {
		key, err := db.ResolveEmployeeKey(t.Context(), other, store.CreateAPIKeyParams{})
		if err != nil || key.ID == first {
			t.Fatalf("identity namespace collision: %+v %v", key, err)
		}
	}
	// The accounting record has no API secret and cannot be used as a token.
	for _, token := range []string{first, "employee:" + first, ""} {
		if _, err := db.FindAPIKeyByPlainText(t.Context(), token); err == nil {
			t.Fatal("accounting identity used as API secret")
		}
	}
	result, err := db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte(`"key":`)) {
		t.Fatal("employee endpoint exposed a key")
	}
}

func TestEmployeeIdentityAndUsageSurviveRestartAndPolicyChange(t *testing.T) {
	dir := t.TempDir()
	open := func() (*store.Store, *store.UsageManager) {
		t.Helper()
		db, err := store.OpenControl(filepath.Join(dir, "control.db"))
		if err != nil {
			t.Fatal(err)
		}
		u, err := store.NewUsageManager(filepath.Join(dir, "usage.db"), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		db.AttachRuntime(u, nil)
		return db, u
	}
	identity := store.EmployeeIdentity{Issuer: "issuer-a", Claim: "staffuserid", Subject: "stable-staff-id"}
	db, usage := open()
	key, err := db.ResolveEmployeeKey(t.Context(), identity, store.CreateAPIKeyParams{RequestQuota: 10})
	if err != nil {
		t.Fatal(err)
	}
	usage.Record(key.ID, 3, 4, 50, time.Now())
	name := "Employee display name"
	if _, err = db.UpdateAPIKey(t.Context(), key.ID, store.APIKeyPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = usage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, usage = open()
	t.Cleanup(func() { _ = usage.Close(context.Background()); _ = db.Close() })
	resolved, err := db.ResolveEmployeeKey(t.Context(), identity, store.CreateAPIKeyParams{RequestQuota: 999})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != key.ID || resolved.Name != name || resolved.RequestQuota != 10 || resolved.UsedRequests != 1 || resolved.UsedTokens != 7 || resolved.UsedMicrocredits != 50 {
		t.Fatalf("employee accounting reset on restart: %+v", resolved)
	}
	results, err := db.SearchAPIKeys(t.Context(), store.APIKeyListParams{Source: "employee", Search: identity.Subject})
	if err != nil || results.Total != 1 {
		t.Fatalf("cannot search employee identity after renaming: %+v %v", results, err)
	}
}
