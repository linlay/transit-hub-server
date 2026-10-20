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
