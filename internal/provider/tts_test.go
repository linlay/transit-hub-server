package provider

import (
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

func TestTTSEndpointsAndTypeContract(t *testing.T) {
	for _, tc := range []struct{ protocol, base, want string }{
		{"minimax_tts", "https://example.test", "/v1/t2a_v2"},
		{"minimax_tts", "https://example.test/v1", "/t2a_v2"},
		{"dashscope_tts", "https://example.test", "/api/v1/services/aigc/multimodal-generation/generation"},
		{"dashscope_tts", "https://example.test/api/v1/", "/services/aigc/multimodal-generation/generation"},
	} {
		cfg := config.ProviderConfig{Name: "tts", Protocol: tc.protocol, BaseURL: tc.base, DefaultPool: "main", Models: []config.ModelConfig{{Public: "speech", Upstream: "speech-upstream", Type: "tts"}}, Pools: []config.PoolConfig{{Name: "main", Accounts: []config.AccountConfig{{Name: "account", APIKey: "key", Weight: 1}}}}}
		reg, err := NewRegistry([]config.ProviderConfig{cfg}, CircuitOptions{FailureThreshold: 1, Cooldown: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		route, ok := reg.Resolve(tc.protocol, "speech")
		if !ok || route.EndpointPath(tc.protocol, "/incorrect") != tc.want {
			t.Fatalf("route=%+v path=%s", route, route.EndpointPath(tc.protocol, "/incorrect"))
		}
		cfg.Endpoints = map[string]string{tc.protocol: "/custom/tts"}
		if err := reg.Replace([]config.ProviderConfig{cfg}); err != nil {
			t.Fatal(err)
		}
		route, _ = reg.Resolve(tc.protocol, "speech")
		if route.EndpointPath(tc.protocol, "") != "/custom/tts" {
			t.Fatal("endpoint override ignored")
		}
		cfg.Models[0].Type = "chat"
		if config.ValidateProviderConfig(cfg) == nil {
			t.Fatal("chat accepted with TTS protocol")
		}
		cfg.Models[0].Type = "tts"
		cfg.Protocol = "openai"
		if config.ValidateProviderConfig(cfg) == nil {
			t.Fatal("tts accepted with chat protocol")
		}
	}
}

func TestModelProtocolOverrideSharesProviderAndAccounts(t *testing.T) {
	cfg := config.ProviderConfig{
		Name: "minimax", Protocol: "openai", BaseURL: "https://api.example.test", DefaultPool: "main",
		Models: []config.ModelConfig{
			{Public: "chat", Upstream: "chat-upstream"},
			{Public: "speech", Upstream: "speech-2.8-hd", Protocol: "minimax_tts", Type: "tts"},
		},
		Usage: config.UsageConfig{"openai_chat_completions": {Input: config.UsagePaths{"custom.input"}}},
		Pools: []config.PoolConfig{{Name: "main", Accounts: []config.AccountConfig{{Name: "shared", APIKey: "key"}}}},
	}
	reg, err := NewRegistry([]config.ProviderConfig{cfg}, CircuitOptions{FailureThreshold: 3, Cooldown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	chat, chatOK := reg.Resolve("openai", "chat")
	speech, speechOK := reg.Resolve("minimax_tts", "speech")
	if !chatOK || !speechOK || chat.Provider != speech.Provider || chat.Pool != speech.Pool || chat.Pool.Accounts[0] != speech.Pool.Accounts[0] {
		t.Fatal("model protocol override duplicated provider, pool or credentials")
	}
	if speech.EndpointPath("minimax_tts", "") != "/v1/t2a_v2" || len(speech.Usage) != 0 || len(chat.Usage) != 1 {
		t.Fatal("model inherited the wrong endpoint or usage mapping")
	}
	if _, ok := reg.Resolve("openai", "speech"); ok {
		t.Fatal("TTS model leaked into chat protocol")
	}
	snapshot := reg.Snapshot(nil)
	if len(snapshot.Providers) != 1 || len(snapshot.Providers[0].Models) != 2 {
		t.Fatal("new provider was created")
	}
	for _, model := range snapshot.Providers[0].Models {
		if model.Public == "speech" && model.Protocol != "minimax_tts" {
			t.Fatal("snapshot lost model protocol")
		}
	}
	_, account, err := reg.ResolveConnectivityTarget(ConnectivityTarget{ProviderName: "minimax", PublicModel: "speech"})
	if err != nil || account != chat.Pool.Accounts[0] {
		t.Fatal("connectivity test did not reuse account")
	}
	for _, tc := range []struct{ protocol, modelType string }{{"unknown", "tts"}, {"openai", "tts"}, {"minimax_tts", "chat"}} {
		cfg.Models[1].Protocol, cfg.Models[1].Type = tc.protocol, tc.modelType
		if config.ValidateProviderConfig(cfg) == nil {
			t.Fatalf("accepted invalid model protocol/type: %+v", tc)
		}
	}
}
