package gateway

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/issuer"
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
)

func TestOpenAIProxyRewritesModelAuthAndRecordsUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream auth: %q", got)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Fatalf("gateway x-api-key leaked upstream: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-model" {
			t.Fatalf("model was not rewritten: %#v", body["model"])
		}
		if body["temperature"].(float64) != 0.2 {
			t.Fatalf("non-model field was not preserved: %#v", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model":"public-model",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0.2
	}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedRequests != 1 {
		t.Fatalf("used_requests = %d", key.UsedRequests)
	}
	if key.UsedTokens != 7 {
		t.Fatalf("used_tokens = %d", key.UsedTokens)
	}
}

func TestAnthropicProxyUsesNativePathAndXAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "upstream-key" {
			t.Fatalf("unexpected upstream x-api-key: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization leaked upstream: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "claude-upstream" {
			t.Fatalf("model was not rewritten: %#v", body["model"])
		}
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"input_tokens":2,"output_tokens":5}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{anthropicProvider(upstream.URL)})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{
		"model":"claude-public",
		"messages":[{"role":"user","content":"hi"}],
		"max_tokens":64
	}`))
	req.Header.Set("x-api-key", plainKey)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedTokens != 7 {
		t.Fatalf("used_tokens = %d", key.UsedTokens)
	}
}

func TestAPIKeyModelWhitelistAllowsDeniesAndPreservesRouteNotFound(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	providerConfig := openAIProvider(upstream.URL)
	providerConfig.Models = append(providerConfig.Models, config.ModelConfig{
		Public:   "other-model",
		Upstream: "other-upstream",
		Pool:     "primary",
	})
	app, db, plainKey := newTestGatewayWithKey(t, []config.ProviderConfig{providerConfig}, store.CreateAPIKeyParams{
		Name:          "limited-models",
		AllowedModels: []string{"public-model"},
	})

	allowedReq := proxyRequestWithModel(plainKey, "public-model")
	allowedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, body = %s", allowedRec.Code, allowedRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits after allowed request = %d", got)
	}

	deniedReq := proxyRequestWithModel(plainKey, "other-model")
	deniedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(deniedRec, deniedReq)
	if deniedRec.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, body = %s", deniedRec.Code, deniedRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream was called for denied model: hits = %d", got)
	}

	missingReq := proxyRequestWithModel(plainKey, "missing-model")
	missingRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing route status = %d, body = %s", missingRec.Code, missingRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream was called for missing route: hits = %d", got)
	}

	key, err := db.FindAPIKeyByPlainText(t.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	foundDeniedLog := false
	for _, log := range logs.Items {
		if log.PublicModel == "other-model" && log.StatusCode == http.StatusForbidden && log.ErrorType == "model_not_allowed" {
			foundDeniedLog = true
			break
		}
	}
	if !foundDeniedLog {
		t.Fatalf("missing model_not_allowed log: %#v", logs.Items)
	}
}

func TestAPIKeyModelWhitelistEmptyDeniesAllModels(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	app, _, plainKey := newTestGatewayWithKey(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{
		Name:          "empty-models",
		AllowedModels: []string{},
	})

	req := proxyRequest(plainKey)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Fatalf("upstream hits = %d", got)
	}
}

func TestQuotaAndForcedExpirationRejectRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGatewayWithKey(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{
		Name:         "limited",
		RequestQuota: 1,
		TokenQuota:   0,
	})

	first := proxyRequest(plainKey)
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := proxyRequest(plainKey)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(second.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	forced := true
	if _, err := db.UpdateAPIKey(second.Context(), key.ID, store.APIKeyPatch{ForcedExpired: &forced}); err != nil {
		t.Fatal(err)
	}

	third := proxyRequest(plainKey)
	thirdRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(thirdRec, third)
	if thirdRec.Code != http.StatusUnauthorized {
		t.Fatalf("third status = %d, body = %s", thirdRec.Code, thirdRec.Body.String())
	}
}

func TestCircuitOpensAfterUpstreamFailure(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})

	first := proxyRequest(plainKey)
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d", firstRec.Code)
	}

	second := proxyRequest(plainKey)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d", got)
	}
}

func TestAdminCreateKeyReturnsPlainTextOnce(t *testing.T) {
	app, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})

	createReq := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{
		"name":"admin-created",
		"request_quota":10,
		"token_quota":100,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if key, _ := created["key"].(string); !strings.HasPrefix(key, "th_") {
		t.Fatalf("plain key missing from create response: %#v", created["key"])
	}
	if allowed, _ := created["allowed_models"].([]any); len(allowed) != 1 || allowed[0] != "public-model" {
		t.Fatalf("unexpected allowed_models: %#v", created["allowed_models"])
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/api-keys", nil)
	listReq.Header.Set("x-admin-token", "admin")
	listRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if bytes.Contains(listRec.Body.Bytes(), []byte(`"key"`)) {
		t.Fatalf("list response leaked plain key: %s", listRec.Body.String())
	}
}

func TestAdminAPIKeyAllowedModelsCreatePatchAndValidation(t *testing.T) {
	providerConfig := openAIProvider("https://upstream.invalid")
	providerConfig.Models = append(providerConfig.Models, config.ModelConfig{
		Public:   "other-model",
		Upstream: "other-upstream",
		Pool:     "primary",
	})
	app, _, _ := newTestGateway(t, []config.ProviderConfig{providerConfig})

	invalidCreateReq := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{
		"name":"invalid",
		"allowed_models":["missing-model"]
	}`))
	invalidCreateReq.Header.Set("Authorization", "Bearer admin")
	invalidCreateRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(invalidCreateRec, invalidCreateReq)
	if invalidCreateRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, body = %s", invalidCreateRec.Code, invalidCreateRec.Body.String())
	}

	emptyCreateReq := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{
		"name":"empty",
		"allowed_models":[]
	}`))
	emptyCreateReq.Header.Set("Authorization", "Bearer admin")
	emptyCreateRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(emptyCreateRec, emptyCreateReq)
	if emptyCreateRec.Code != http.StatusBadRequest {
		t.Fatalf("empty create status = %d, body = %s", emptyCreateRec.Code, emptyCreateRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{
		"name":"model-scoped",
		"allowed_models":[" public-model ","other-model","public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID            string   `json:"id"`
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || strings.Join(created.AllowedModels, ",") != "other-model,public-model" {
		t.Fatalf("unexpected created key allowed_models: %#v", created)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/admin/api-keys/"+created.ID, bytes.NewBufferString(`{
		"allowed_models":["public-model"]
	}`))
	patchReq.Header.Set("Authorization", "Bearer admin")
	patchRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	var patched struct {
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if strings.Join(patched.AllowedModels, ",") != "public-model" {
		t.Fatalf("unexpected patched allowed_models: %#v", patched.AllowedModels)
	}

	invalidPatchReq := httptest.NewRequest(http.MethodPatch, "/admin/api-keys/"+created.ID, bytes.NewBufferString(`{
		"allowed_models":["missing-model"]
	}`))
	invalidPatchReq.Header.Set("Authorization", "Bearer admin")
	invalidPatchRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(invalidPatchRec, invalidPatchReq)
	if invalidPatchRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid patch status = %d, body = %s", invalidPatchRec.Code, invalidPatchRec.Body.String())
	}

	clearReq := httptest.NewRequest(http.MethodPatch, "/admin/api-keys/"+created.ID, bytes.NewBufferString(`{
		"allowed_models":[]
	}`))
	clearReq.Header.Set("Authorization", "Bearer admin")
	clearRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusBadRequest {
		t.Fatalf("clear status = %d, body = %s", clearRec.Code, clearRec.Body.String())
	}
}

func TestJWTGrantIssuesDesktopAPIKeys(t *testing.T) {
	issuerSvc := newTestIssuer(t)
	app, db, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	emptyCreateReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"empty grant",
		"issue_quota":1,
		"allowed_models":[]
	}`))
	emptyCreateReq.Header.Set("Authorization", "Bearer admin")
	emptyCreateRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(emptyCreateRec, emptyCreateReq)
	if emptyCreateRec.Code != http.StatusBadRequest {
		t.Fatalf("empty grant create status = %d, body = %s", emptyCreateRec.Code, emptyCreateRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"desktop grant",
		"issue_quota":1,
		"request_quota":25,
		"token_quota":2500,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("grant create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var grantPayload map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &grantPayload); err != nil {
		t.Fatal(err)
	}
	token, _ := grantPayload["jwt"].(string)
	jti, _ := grantPayload["jti"].(string)
	if token == "" || jti == "" {
		t.Fatalf("grant response missing token or jti: %#v", grantPayload)
	}
	if grantPayload["request_quota"].(float64) != 25 || grantPayload["token_quota"].(float64) != 2500 {
		t.Fatalf("grant response missing quotas: %#v", grantPayload)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/jwt-grants", nil)
	listReq.Header.Set("Authorization", "Bearer admin")
	listRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("grant list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if bytes.Contains(listRec.Body.Bytes(), []byte(`"jwt"`)) {
		t.Fatalf("grant list leaked jwt: %s", listRec.Body.String())
	}
	if !bytes.Contains(listRec.Body.Bytes(), []byte(`"allowed_models":["public-model"]`)) {
		t.Fatalf("grant list missing allowed_models: %s", listRec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/admin/jwt-grants/"+jti, nil)
	detailReq.Header.Set("Authorization", "Bearer admin")
	detailRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("grant detail status = %d, body = %s", detailRec.Code, detailRec.Body.String())
	}
	var detailPayload map[string]any
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detailPayload); err != nil {
		t.Fatal(err)
	}
	if detailPayload["jwt"] != token {
		t.Fatalf("grant detail jwt mismatch: %#v", detailPayload["jwt"])
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"desktop","description":"ignored"}`))
	applyReq.Header.Set("Authorization", "Bearer "+token)
	applyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, body = %s", applyRec.Code, applyRec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(applyRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	plainKey, _ := created["key"].(string)
	if !strings.HasPrefix(plainKey, "dk_") {
		t.Fatalf("expected dk key, got %#v", created["key"])
	}
	key, err := db.FindAPIKeyByPlainText(t.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.Source != "jwt" || key.IssuerJTI != jti || key.RequestQuota != 25 || key.TokenQuota != 2500 || strings.Join(key.AllowedModels, ",") != "public-model" {
		t.Fatalf("unexpected issued key: %#v", key)
	}
	if key.Description != "" {
		t.Fatalf("description should be ignored, got %q", key.Description)
	}
	filterReq := httptest.NewRequest(http.MethodGet, "/admin/api-keys?source=jwt&issuer_jti="+jti, nil)
	filterReq.Header.Set("Authorization", "Bearer admin")
	filterRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(filterRec, filterReq)
	if filterRec.Code != http.StatusOK {
		t.Fatalf("api key filter status = %d, body = %s", filterRec.Code, filterRec.Body.String())
	}
	var filtered struct {
		Items []struct {
			ID        string `json:"id"`
			Source    string `json:"source"`
			IssuerJTI string `json:"issuer_jti"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(filterRec.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].Source != "jwt" || filtered.Items[0].IssuerJTI != jti {
		t.Fatalf("unexpected filtered api keys: %#v", filtered)
	}
	grant, err := db.GetJWTGrant(t.Context(), jti)
	if err != nil {
		t.Fatal(err)
	}
	if grant.IssuedCount != 1 {
		t.Fatalf("issued_count = %d", grant.IssuedCount)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{}`))
	secondReq.Header.Set("Authorization", "Bearer "+token)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("second apply status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
}

func TestJWTGrantPatchAffectsFutureIssuedKeysOnly(t *testing.T) {
	issuerSvc := newTestIssuer(t)
	providerConfig := openAIProvider("https://upstream.invalid")
	providerConfig.Models = append(providerConfig.Models, config.ModelConfig{
		Public:   "other-model",
		Upstream: "other-upstream",
		Pool:     "primary",
	})
	app, db, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{providerConfig}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	createReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"patchable grant",
		"issue_quota":2,
		"request_quota":10,
		"token_quota":100,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("grant create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createdGrant struct {
		JTI string `json:"jti"`
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createdGrant); err != nil {
		t.Fatal(err)
	}

	firstReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"first"}`))
	firstReq.Header.Set("Authorization", "Bearer "+createdGrant.JWT)
	firstRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first apply status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}
	var firstPayload struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	firstKey, err := db.FindAPIKeyByPlainText(t.Context(), firstPayload.Key)
	if err != nil {
		t.Fatal(err)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/admin/jwt-grants/"+createdGrant.JTI, bytes.NewBufferString(`{
		"request_quota":20,
		"token_quota":200,
		"allowed_models":["other-model"]
	}`))
	patchReq.Header.Set("Authorization", "Bearer admin")
	patchRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch grant status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}

	emptyPatchReq := httptest.NewRequest(http.MethodPatch, "/admin/jwt-grants/"+createdGrant.JTI, bytes.NewBufferString(`{
		"allowed_models":[]
	}`))
	emptyPatchReq.Header.Set("Authorization", "Bearer admin")
	emptyPatchRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(emptyPatchRec, emptyPatchReq)
	if emptyPatchRec.Code != http.StatusBadRequest {
		t.Fatalf("empty grant patch status = %d, body = %s", emptyPatchRec.Code, emptyPatchRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"second"}`))
	secondReq.Header.Set("Authorization", "Bearer "+createdGrant.JWT)
	secondRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("second apply status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	var secondPayload struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(secondRec.Body.Bytes(), &secondPayload); err != nil {
		t.Fatal(err)
	}
	secondKey, err := db.FindAPIKeyByPlainText(t.Context(), secondPayload.Key)
	if err != nil {
		t.Fatal(err)
	}

	if firstKey.RequestQuota != 10 || firstKey.TokenQuota != 100 || strings.Join(firstKey.AllowedModels, ",") != "public-model" {
		t.Fatalf("first key changed unexpectedly: %#v", firstKey)
	}
	if secondKey.RequestQuota != 20 || secondKey.TokenQuota != 200 || strings.Join(secondKey.AllowedModels, ",") != "other-model" {
		t.Fatalf("second key did not use patched grant: %#v", secondKey)
	}
}

func TestDeleteJWTGrantDoesNotDeleteIssuedAPIKeys(t *testing.T) {
	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	issuerSvc := newTestIssuer(t)
	app, _, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{openAIProvider(upstream.URL)}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	createReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"deletable grant",
		"issue_quota":2,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("grant create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var grant struct {
		JTI string `json:"jti"`
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"issued-before-delete"}`))
	applyReq.Header.Set("Authorization", "Bearer "+grant.JWT)
	applyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, body = %s", applyRec.Code, applyRec.Body.String())
	}
	var issued struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/jwt-grants/"+grant.JTI, nil)
	deleteReq.Header.Set("Authorization", "Bearer admin")
	deleteRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete grant status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	proxyReq := proxyRequest(issued.Key)
	proxyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(proxyRec, proxyReq)
	if proxyRec.Code != http.StatusOK {
		t.Fatalf("issued key proxy status = %d, body = %s", proxyRec.Code, proxyRec.Body.String())
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d", got)
	}

	secondApplyReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"after-delete"}`))
	secondApplyReq.Header.Set("Authorization", "Bearer "+grant.JWT)
	secondApplyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondApplyRec, secondApplyReq)
	if secondApplyRec.Code != http.StatusForbidden {
		t.Fatalf("apply after delete status = %d, body = %s", secondApplyRec.Code, secondApplyRec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/admin/jwt-grants/"+grant.JTI, nil)
	detailReq.Header.Set("Authorization", "Bearer admin")
	detailRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("detail after delete status = %d, body = %s", detailRec.Code, detailRec.Body.String())
	}
}

func TestDeleteJWTGrantCanDeleteIssuedAPIKeys(t *testing.T) {
	issuerSvc := newTestIssuer(t)
	app, db, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	createReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"delete issued keys",
		"issue_quota":2,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("grant create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var grant struct {
		JTI string `json:"jti"`
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{"name":"issued-before-delete"}`))
	applyReq.Header.Set("Authorization", "Bearer "+grant.JWT)
	applyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, body = %s", applyRec.Code, applyRec.Body.String())
	}
	var issued struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/jwt-grants/"+grant.JTI+"?delete_api_keys=true", nil)
	deleteReq.Header.Set("Authorization", "Bearer admin")
	deleteRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete grant status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	key, err := db.GetAPIKey(t.Context(), issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if key.Status != "disabled" || !key.ForcedExpired || key.DeletedAt == nil {
		t.Fatalf("issued key was not soft-deleted: %#v", key)
	}
	proxyReq := proxyRequest(issued.Key)
	proxyRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(proxyRec, proxyReq)
	if proxyRec.Code != http.StatusUnauthorized {
		t.Fatalf("issued key proxy status = %d, body = %s", proxyRec.Code, proxyRec.Body.String())
	}
}

func TestJWTGrantListFilters(t *testing.T) {
	issuerSvc := newTestIssuer(t)
	app, db, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	createReq := httptest.NewRequest(http.MethodPost, "/admin/jwt-grants", bytes.NewBufferString(`{
		"name":"desktop rollout",
		"description":"mac clients",
		"issue_quota":10,
		"allowed_models":["public-model"]
	}`))
	createReq.Header.Set("Authorization", "Bearer admin")
	createRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("grant create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var activeGrant struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &activeGrant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateJWTGrant(t.Context(), store.CreateJWTGrantParams{
		JTI:           store.GenerateJTI(),
		Name:          "archived rollout",
		Status:        "disabled",
		IssueQuota:    10,
		AllowedModels: []string{"public-model"},
	}); err != nil {
		t.Fatal(err)
	}

	filterReq := httptest.NewRequest(http.MethodGet, "/admin/jwt-grants?status=active&search=desktop", nil)
	filterReq.Header.Set("Authorization", "Bearer admin")
	filterRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(filterRec, filterReq)
	if filterRec.Code != http.StatusOK {
		t.Fatalf("grant filter status = %d, body = %s", filterRec.Code, filterRec.Body.String())
	}
	var filtered struct {
		Items []struct {
			JTI    string `json:"jti"`
			Status string `json:"status"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(filterRec.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].JTI != activeGrant.JTI || filtered.Items[0].Status != "active" {
		t.Fatalf("unexpected filtered grants: %#v", filtered)
	}

	jtiReq := httptest.NewRequest(http.MethodGet, "/admin/jwt-grants?search="+activeGrant.JTI, nil)
	jtiReq.Header.Set("Authorization", "Bearer admin")
	jtiRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(jtiRec, jtiReq)
	if jtiRec.Code != http.StatusOK || !bytes.Contains(jtiRec.Body.Bytes(), []byte(activeGrant.JTI)) {
		t.Fatalf("jti search failed status = %d, body = %s", jtiRec.Code, jtiRec.Body.String())
	}
}

func TestApplyAPIKeyRejectsInvalidExpiredDisabledAndMissingIssuer(t *testing.T) {
	missingIssuerApp, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	missingReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{}`))
	missingReq.Header.Set("Authorization", "Bearer invalid")
	missingRec := httptest.NewRecorder()
	missingIssuerApp.Handler().ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing issuer status = %d, body = %s", missingRec.Code, missingRec.Body.String())
	}

	issuerSvc := newTestIssuer(t)
	app, db, _ := newTestGatewayWithKeyAndIssuer(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")}, store.CreateAPIKeyParams{Name: "test"}, issuerSvc)

	jti := store.GenerateJTI()
	expiresAt := time.Now().UTC().Add(time.Hour)
	token, err := issuerSvc.SignGrant(jti, expiresAt, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateJWTGrant(t.Context(), store.CreateJWTGrantParams{
		JTI:        jti,
		Name:       "disabled",
		IssueQuota: 10,
		ExpiresAt:  &expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	tokenParts := strings.Split(token, ".")
	if len(tokenParts) != 3 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	tampered := tokenParts[0] + "." + tokenParts[1] + ".invalid-signature"
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{}`))
	invalidReq.Header.Set("Authorization", "Bearer "+tampered)
	invalidRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid jwt status = %d, body = %s", invalidRec.Code, invalidRec.Body.String())
	}

	disabled := "disabled"
	if _, err := db.UpdateJWTGrant(t.Context(), jti, store.JWTGrantPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	disabledReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{}`))
	disabledReq.Header.Set("Authorization", "Bearer "+token)
	disabledRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(disabledRec, disabledReq)
	if disabledRec.Code != http.StatusForbidden {
		t.Fatalf("disabled grant status = %d, body = %s", disabledRec.Code, disabledRec.Body.String())
	}

	expiredJTI := store.GenerateJTI()
	expiredAt := time.Now().UTC().Add(-time.Hour)
	expiredToken, err := issuerSvc.SignGrant(expiredJTI, expiredAt, time.Now().UTC().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateJWTGrant(t.Context(), store.CreateJWTGrantParams{
		JTI:        expiredJTI,
		Name:       "expired",
		IssueQuota: 10,
		ExpiresAt:  &expiredAt,
	}); err != nil {
		t.Fatal(err)
	}
	expiredReq := httptest.NewRequest(http.MethodPost, "/api/apply-apikey", bytes.NewBufferString(`{}`))
	expiredReq.Header.Set("Authorization", "Bearer "+expiredToken)
	expiredRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(expiredRec, expiredReq)
	if expiredRec.Code != http.StatusUnauthorized {
		t.Fatalf("expired jwt status = %d, body = %s", expiredRec.Code, expiredRec.Body.String())
	}
}

func TestAdminLoginCookieAuthorizesRequests(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	if _, err := db.CreateAdminUser(t.Context(), store.CreateAdminUserParams{
		Username: "operator",
		Password: "secret-pass",
	}); err != nil {
		t.Fatal(err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/login", bytes.NewBufferString(`{
		"username":"operator",
		"password":"secret-pass"
	}`))
	loginReq.Header.Set("Origin", "http://localhost:5173")
	loginRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	if got := loginRec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("cors credentials header = %q", got)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != adminSessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("session cookie not set correctly: %#v", cookies)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/admin/auth/me", nil)
	meReq.AddCookie(cookies[0])
	meRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", meRec.Code, meRec.Body.String())
	}
	if !bytes.Contains(meRec.Body.Bytes(), []byte(`"username":"operator"`)) {
		t.Fatalf("me response missing user: %s", meRec.Body.String())
	}
}

func TestSoftDeletedAPIKeyCannotProxyButHistoryRemains(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	req := proxyRequest(plainKey)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}
	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api-keys/"+key.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer admin")
	deleteRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	blockedReq := proxyRequest(plainKey)
	blockedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusUnauthorized {
		t.Fatalf("blocked status = %d, body = %s", blockedRec.Code, blockedRec.Body.String())
	}

	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 {
		t.Fatalf("history logs total = %d", logs.Total)
	}
}

func TestBatchAPIKeysInactiveByIDs(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	created, err := db.CreateAPIKey(t.Context(), store.CreateAPIKeyParams{
		Name:          "batch inactive",
		AllowedModels: []string{"public-model"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/batch", bytes.NewBufferString(`{
		"action":"inactive",
		"ids":["`+created.ID+`"]
	}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch inactive status = %d, body = %s", rec.Code, rec.Body.String())
	}
	key, err := db.GetAPIKey(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if key.Status != "disabled" || key.ForcedExpired || key.DeletedAt != nil {
		t.Fatalf("unexpected inactive key state: %#v", key)
	}
}

func TestBatchAPIKeysDeleteByIssuerJTI(t *testing.T) {
	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	jti := store.GenerateJTI()
	created, err := db.CreateAPIKey(t.Context(), store.CreateAPIKeyParams{
		Name:          "issued key",
		Source:        "jwt",
		IssuerJTI:     jti,
		AllowedModels: []string{"public-model"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/batch", bytes.NewBufferString(`{
		"action":"delete",
		"issuer_jti":"`+jti+`"
	}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result store.APIKeyBatchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Matched != 1 || result.Updated != 1 {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	key, err := db.GetAPIKey(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if key.Status != "disabled" || !key.ForcedExpired || key.DeletedAt == nil {
		t.Fatalf("unexpected deleted key state: %#v", key)
	}
}

func TestBatchAPIKeysRequiresSelector(t *testing.T) {
	app, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://upstream.invalid")})
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/batch", bytes.NewBufferString(`{"action":"delete"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("batch empty selector status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestProxyRecordsSessionAndEstimatedCost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{
		Protocol:                      "openai",
		PublicModel:                   "public-model",
		InputCostMicroUSDPer1MTokens:  2_000_000,
		OutputCostMicroUSDPer1MTokens: 4_000_000,
		Currency:                      "USD",
	}); err != nil {
		t.Fatal(err)
	}

	req := proxyRequest(plainKey)
	req.Header.Set("x-device-id", "macbook-pro")
	req.Header.Set("x-source", "codex")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 || logs.Items[0].DeviceID != "macbook-pro" || logs.Items[0].Source != "codex" {
		t.Fatalf("unexpected log session fields: %#v", logs)
	}
	if logs.Items[0].CostMicroUSD != 22 {
		t.Fatalf("cost_microusd = %d", logs.Items[0].CostMicroUSD)
	}
	sessions, err := db.ListAPISessions(t.Context(), store.APISessionQuery{
		APIKeyID:     key.ID,
		ActiveWindow: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessions.Total != 1 || !sessions.Items[0].Active || sessions.Items[0].TokenCount != 7 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestProxyRecordsDeepSeekCacheAndProviderUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}}`))
	}))
	defer upstream.Close()

	app, db, plainKey := newTestGateway(t, []config.ProviderConfig{openAIProvider(upstream.URL)})
	inputCacheHitCost := int64(500_000)
	if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{
		Protocol:                             "openai",
		PublicModel:                          "public-model",
		InputCostMicroUSDPer1MTokens:         2_000_000,
		InputCacheHitCostMicroUSDPer1MTokens: &inputCacheHitCost,
		OutputCostMicroUSDPer1MTokens:        4_000_000,
		Currency:                             "USD",
	}); err != nil {
		t.Fatal(err)
	}

	req := proxyRequest(plainKey)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}

	key, err := db.FindAPIKeyByPlainText(req.Context(), plainKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedTokens != 15 {
		t.Fatalf("used_tokens = %d", key.UsedTokens)
	}
	logs, err := db.ListRequestLogs(t.Context(), store.RequestLogQuery{APIKeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 {
		t.Fatalf("logs total = %d", logs.Total)
	}
	log := logs.Items[0]
	if log.CacheHitTokens != 4 || log.CacheMissTokens != 6 || log.CacheTotalTokens != 10 {
		t.Fatalf("unexpected cache tokens: %#v", log)
	}
	if log.CacheHitRate == nil || math.Abs(*log.CacheHitRate-0.4) > 0.0001 {
		t.Fatalf("cache hit rate = %#v", log.CacheHitRate)
	}
	if log.CostMicroUSD != 34 {
		t.Fatalf("cost_microusd = %d", log.CostMicroUSD)
	}

	traffic, err := db.Traffic(t.Context(), store.TrafficQuery{APIKeyID: key.ID, Bucket: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].Requests != 1 || traffic[0].TotalTokens != 15 || traffic[0].CacheHitTokens != 4 || traffic[0].CacheMissTokens != 6 {
		t.Fatalf("unexpected traffic: %#v", traffic)
	}

	providers, err := db.ProviderUsage(t.Context(), store.ProviderUsageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider usage count = %d: %#v", len(providers), providers)
	}
	if providers[0].Provider != "test-openai" || providers[0].Requests != 1 || providers[0].TotalTokens != 15 || providers[0].CacheHitTokens != 4 || providers[0].CostMicroUSD != 34 {
		t.Fatalf("unexpected provider usage: %#v", providers[0])
	}
	accountUsage, err := db.ProviderAccountUsage(t.Context(), store.ProviderUsageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(accountUsage) != 1 {
		t.Fatalf("provider account usage count = %d: %#v", len(accountUsage), accountUsage)
	}
	if accountUsage[0].Provider != "test-openai" || accountUsage[0].Pool != "primary" || accountUsage[0].Account != "acct1" || accountUsage[0].Requests != 1 || accountUsage[0].TotalTokens != 15 {
		t.Fatalf("unexpected provider account usage: %#v", accountUsage[0])
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/admin/providers/usage", nil)
	usageReq.Header.Set("Authorization", "Bearer admin")
	usageRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(usageRec, usageReq)
	if usageRec.Code != http.StatusOK {
		t.Fatalf("provider usage status = %d, body = %s", usageRec.Code, usageRec.Body.String())
	}
	var usagePayload struct {
		AccountItems []store.ProviderAccountUsage `json:"account_items"`
	}
	if err := json.Unmarshal(usageRec.Body.Bytes(), &usagePayload); err != nil {
		t.Fatal(err)
	}
	if len(usagePayload.AccountItems) != 1 || usagePayload.AccountItems[0].Account != "acct1" {
		t.Fatalf("unexpected provider usage response: %s", usageRec.Body.String())
	}
}

func newTestGateway(t *testing.T, providers []config.ProviderConfig) (*Gateway, *store.Store, string) {
	t.Helper()
	return newTestGatewayWithKey(t, providers, store.CreateAPIKeyParams{Name: "test"})
}

func newTestGatewayWithKey(t *testing.T, providers []config.ProviderConfig, keyParams store.CreateAPIKeyParams) (*Gateway, *store.Store, string) {
	t.Helper()
	return newTestGatewayWithKeyAndIssuer(t, providers, keyParams, nil)
}

func newTestGatewayWithKeyAndIssuer(t *testing.T, providers []config.ProviderConfig, keyParams store.CreateAPIKeyParams, issuerSvc *issuer.Service) (*Gateway, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	registry, err := provider.NewRegistry(providers, provider.CircuitOptions{
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if keyParams.AllowedModels == nil {
		keyParams.AllowedModels = publicModelsFromConfigs(providers)
	}
	created, err := db.CreateAPIKey(t.Context(), keyParams)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		Env: config.Env{
			AdminToken:              "admin",
			AdminSessionTTL:         24 * time.Hour,
			CORSAllowedOrigins:      []string{"http://localhost:5173"},
			SessionActiveWindow:     5 * time.Minute,
			ConfigDir:               t.TempDir(),
			UpstreamTimeout:         3 * time.Second,
			CircuitFailureThreshold: 1,
			CircuitCooldown:         time.Hour,
		},
		Store:    db,
		Issuer:   issuerSvc,
		Registry: registry,
		Client:   upstreamClient(t),
	})
	return app, db, created.PlainText
}

func publicModelsFromConfigs(providers []config.ProviderConfig) []string {
	seen := map[string]struct{}{}
	models := []string{}
	for _, providerConfig := range providers {
		for _, model := range providerConfig.Models {
			if _, exists := seen[model.Public]; exists {
				continue
			}
			seen[model.Public] = struct{}{}
			models = append(models, model.Public)
		}
	}
	return models
}

func newTestIssuer(t *testing.T) *issuer.Service {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "private.pem")
	publicPath := filepath.Join(dir, "public.pem")
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})
	if err := os.WriteFile(privatePath, privatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, publicPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	service, err := issuer.New(config.IssuerConfig{
		PrivateKeyPath:            privatePath,
		PublicKeyPath:             publicPath,
		Issuer:                    "test-issuer",
		Audience:                  "test-audience",
		DefaultJWTTTL:             time.Hour,
		DefaultAPIKeyRequestQuota: 500,
		DefaultAPIKeyTokenQuota:   2000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func upstreamClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 3 * time.Second}
}

func proxyRequest(plainKey string) *http.Request {
	return proxyRequestWithModel(plainKey, "public-model")
}

func proxyRequestWithModel(plainKey, model string) *http.Request {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	return req
}

func openAIProvider(baseURL string) config.ProviderConfig {
	return config.ProviderConfig{
		Name:        "test-openai",
		Protocol:    "openai",
		BaseURL:     baseURL,
		DefaultPool: "primary",
		Models: []config.ModelConfig{{
			Public:   "public-model",
			Upstream: "upstream-model",
			Pool:     "primary",
		}},
		Pools: []config.PoolConfig{{
			Name: "primary",
			Accounts: []config.AccountConfig{{
				Name:   "acct1",
				APIKey: "upstream-key",
				Weight: 1,
			}},
		}},
	}
}

func anthropicProvider(baseURL string) config.ProviderConfig {
	return config.ProviderConfig{
		Name:        "test-anthropic",
		Protocol:    "anthropic",
		BaseURL:     baseURL,
		DefaultPool: "primary",
		Models: []config.ModelConfig{{
			Public:   "claude-public",
			Upstream: "claude-upstream",
			Pool:     "primary",
		}},
		Pools: []config.PoolConfig{{
			Name: "primary",
			Accounts: []config.AccountConfig{{
				Name:   "acct1",
				APIKey: "upstream-key",
				Weight: 1,
			}},
		}},
	}
}
