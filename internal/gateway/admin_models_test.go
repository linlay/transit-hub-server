package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
)

func TestAdminModelsListAndDetail(t *testing.T) {
	openAI := config.ProviderConfig{
		Name:        "openai-provider",
		Protocol:    "openai",
		BaseURL:     "https://user:password@openai.invalid/root?token=base-secret",
		DefaultPool: "primary",
		Headers:     map[string]string{"X-Provider-Secret": "header-secret"},
		Endpoints: map[string]string{
			"openai_chat_completions": "/custom/chat",
			"openai_embeddings":       "/custom/embeddings",
		},
		Models: []config.ModelConfig{
			{Public: "z-chat", Upstream: "upstream-chat", Pool: "primary", OwnedBy: "model-owner", DisplayName: "Z Chat", CreatedAt: "2026-07-01T02:03:04Z"},
			{Public: "a-embedding", Upstream: "upstream-embedding", Pool: "primary", Type: config.ModelTypeEmbedding},
			{Public: "image-model", Upstream: "upstream-image", Pool: "primary", Type: config.ModelTypeImageGeneration, Image: config.ImageModelConfig{EndpointPath: "/custom/images"}},
			{Public: "shared-model", Upstream: "openai-shared", Pool: "primary"},
		},
		Pools: []config.PoolConfig{
			{Name: "primary", Accounts: []config.AccountConfig{{Name: "primary-account", APIKey: "upstream-super-secret", Weight: 1, Headers: map[string]string{"X-Account-Secret": "account-header-secret"}}}},
			{Name: "fallback", Accounts: []config.AccountConfig{{Name: "fallback-account", APIKey: "fallback-secret", Weight: 1}}},
		},
	}
	anthropic := config.ProviderConfig{
		Name:        "anthropic-provider",
		Protocol:    "anthropic",
		BaseURL:     "https://anthropic.invalid/api",
		DefaultPool: "primary",
		Endpoints:   map[string]string{"anthropic_messages": "/custom/messages"},
		Models: []config.ModelConfig{
			{Public: "shared-model", Upstream: "claude-shared", Pool: "primary", DisplayName: "Shared Claude"},
		},
		Pools: []config.PoolConfig{{Name: "primary", Accounts: []config.AccountConfig{{Name: "anthropic-account", APIKey: "anthropic-secret", Weight: 1}}}},
	}

	app, db, _ := newTestGateway(t, []config.ProviderConfig{openAI, anthropic})
	if err := db.SetRouteOverride(t.Context(), "z-chat", "fallback"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRouteOverride(t.Context(), "a-embedding", "missing-pool"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"password", "base-secret", "header-secret", "account-header-secret", "upstream-super-secret", "fallback-secret", "anthropic-secret"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
		}
	}

	var list adminModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 5 {
		t.Fatalf("items = %d, want 5: %#v", len(list.Items), list.Items)
	}
	wantOrder := []string{"a-embedding:openai", "image-model:openai", "shared-model:anthropic", "shared-model:openai", "z-chat:openai"}
	for i, want := range wantOrder {
		got := list.Items[i].PublicModel + ":" + list.Items[i].Protocol
		if got != want {
			t.Fatalf("item %d = %q, want %q", i, got, want)
		}
	}

	byKey := make(map[string]adminModelResponse, len(list.Items))
	for _, item := range list.Items {
		byKey[item.Protocol+":"+item.PublicModel] = item
	}
	embedding := byKey["openai:a-embedding"]
	if embedding.GatewayPath != "/v1/embeddings" || embedding.EndpointKey != "openai_embeddings" || embedding.UpstreamPath != "/custom/embeddings" {
		t.Fatalf("unexpected embedding endpoint: %#v", embedding)
	}
	if embedding.DisplayName != "a-embedding" || embedding.OwnedBy != "openai-provider" || embedding.CreatedAt != "1970-01-01T00:00:00Z" {
		t.Fatalf("unexpected metadata defaults: %#v", embedding)
	}
	if embedding.OverridePool != "missing-pool" || embedding.OverrideValid || embedding.EffectivePool != "" {
		t.Fatalf("unexpected invalid override: %#v", embedding)
	}
	image := byKey["openai:image-model"]
	if image.GatewayPath != "/v1/images/generations" || image.UpstreamPath != "/custom/images" {
		t.Fatalf("unexpected image endpoint: %#v", image)
	}
	claude := byKey["anthropic:shared-model"]
	if claude.GatewayPath != "/v1/messages" || claude.EndpointKey != "anthropic_messages" || claude.UpstreamPath != "/custom/messages" {
		t.Fatalf("unexpected anthropic endpoint: %#v", claude)
	}
	chat := byKey["openai:z-chat"]
	if chat.ProviderBaseURL != "https://openai.invalid/root" || chat.UpstreamURL != "https://openai.invalid/root/custom/chat" {
		t.Fatalf("unexpected sanitized URLs: %#v", chat)
	}
	if chat.OverridePool != "fallback" || !chat.OverrideValid || chat.EffectivePool != "fallback" {
		t.Fatalf("unexpected valid override: %#v", chat)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/admin/models/detail?protocol=openai&public_model=z-chat", nil)
	detailReq.Header.Set("x-admin-token", "admin")
	detailRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detailRec.Code, detailRec.Body.String())
	}
	var detail adminModelResponse
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.PublicModel != "z-chat" || detail.DisplayName != "Z Chat" || detail.OwnedBy != "model-owner" || detail.CreatedAt != "2026-07-01T02:03:04Z" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
}

func TestAdminModelsRequireAuthAndReturnNotFound(t *testing.T) {
	app, _, _ := newTestGateway(t, []config.ProviderConfig{openAIProvider("https://openai.invalid")})

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	unauthorizedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRec.Code, http.StatusUnauthorized)
	}

	notFoundReq := httptest.NewRequest(http.MethodGet, "/admin/models/detail?protocol=openai&public_model=missing", nil)
	notFoundReq.Header.Set("Authorization", "Bearer admin")
	notFoundRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(notFoundRec, notFoundReq)
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d, body = %s", notFoundRec.Code, notFoundRec.Body.String())
	}

	badRequestReq := httptest.NewRequest(http.MethodGet, "/admin/models/detail?protocol=openai", nil)
	badRequestReq.Header.Set("Authorization", "Bearer admin")
	badRequestRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(badRequestRec, badRequestReq)
	if badRequestRec.Code != http.StatusBadRequest {
		t.Fatalf("bad request status = %d, body = %s", badRequestRec.Code, badRequestRec.Body.String())
	}
}
