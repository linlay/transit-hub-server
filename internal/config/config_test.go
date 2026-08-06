package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEnvSupportsThreeDatabasePathsAndLegacyAlias(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "test-admin-token")
	t.Setenv("DB_PATH", "/tmp/legacy-control.db")
	t.Setenv("CONTROL_DB_PATH", "")
	t.Setenv("USAGE_DB_PATH", "/tmp/usage.db")
	t.Setenv("TELEMETRY_DB_PATH", "/tmp/telemetry.db")
	t.Setenv("TELEMETRY_RETENTION", "48h")

	env, err := LoadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env.ControlDBPath != "/tmp/legacy-control.db" || env.DBPath != "/tmp/legacy-control.db" {
		t.Fatalf("legacy DB_PATH alias was not applied: %#v", env)
	}
	if env.UsageDBPath != "/tmp/usage.db" || env.TelemetryDBPath != "/tmp/telemetry.db" {
		t.Fatalf("independent database paths were not applied: %#v", env)
	}
	if env.TelemetryRetention != 48*time.Hour {
		t.Fatalf("telemetry retention=%s want=48h", env.TelemetryRetention)
	}
}

func TestLoadProviderConfigsSkipsExamples(t *testing.T) {
	dir := t.TempDir()
	providersDir := filepath.Join(dir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	example := `
name: example
protocol: openai
base_url: https://example.invalid
models: []
pools: []
`
	if err := os.WriteFile(filepath.Join(providersDir, "deepseek.example.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "issuer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "issuer", "config.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}

	realConfig := `
name: deepseek
protocol: openai
base_url: https://api.deepseek.com
default_pool: primary
models:
  - public: example-chat
    upstream: example-upstream-chat
pools:
  - name: primary
    accounts:
      - name: main
        api_key: sk-test
        weight: 1
`
	if err := os.WriteFile(filepath.Join(providersDir, "deepseek.yaml"), []byte(realConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadProviderConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("configs len = %d", len(configs))
	}
	if configs[0].Name != "deepseek" {
		t.Fatalf("loaded wrong config: %#v", configs[0])
	}

	directConfigs, err := LoadProviderConfigs(providersDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(directConfigs) != 1 {
		t.Fatalf("direct configs len = %d", len(directConfigs))
	}
}

func TestLoadProviderConfigsSupportsModelTypesAndImageEndpoint(t *testing.T) {
	dir := t.TempDir()
	providersDir := filepath.Join(dir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}

	raw := `
name: babelark
protocol: openai
base_url: https://api.babelark.com
default_pool: primary
quota:
  url: https://quota.babelark.com/v1/limits
  interval: 10m
  items_path: data.limits
  fields:
    model_name: model.name
    current_remaining_percent: usage.remaining_ratio
  scales:
    current_remaining_percent: 100
  status_values:
    ready: normal
models:
  - public: babelark-embedding
    upstream: text-embedding-v4
    type: embedding
  - public: babelark-image
    upstream: gemini-3.1-flash-image-preview
    type: image-generation
    image:
      endpointPath: /v1/images/generations
pools:
  - name: primary
    accounts:
      - name: main
        api_key: sk-test
        weight: 1
`
	if err := os.WriteFile(filepath.Join(providersDir, "babelark.yaml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	configs, err := LoadProviderConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || len(configs[0].Models) != 2 {
		t.Fatalf("unexpected configs: %#v", configs)
	}
	if configs[0].Quota == nil || configs[0].Quota.ItemsPath != "data.limits" || configs[0].Quota.Fields["model_name"] != "model.name" || configs[0].Quota.Scales["current_remaining_percent"] != 100 {
		t.Fatalf("unexpected quota config: %#v", configs[0].Quota)
	}
	if configs[0].Models[0].Type != ModelTypeEmbedding {
		t.Fatalf("embedding model type = %q", configs[0].Models[0].Type)
	}
	if got := configs[0].Models[1].Image.EndpointPathValue(); got != "/v1/images/generations" {
		t.Fatalf("image endpoint path = %q", got)
	}
}

func TestLoadProviderConfigsResolvesAccountAPIKeyFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	providersDir := filepath.Join(dir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BAILIAN_API_KEY", "sk-test-from-environment")

	raw := `
name: bailian
protocol: openai
base_url: https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
models:
  - public: bailian-qwen3_7-plus
    upstream: qwen3.7-plus
pools:
  - name: primary
    accounts:
      - name: token-plan
        api_key_env: BAILIAN_API_KEY
`
	if err := os.WriteFile(filepath.Join(providersDir, "bailian.yaml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadProviderConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := configs[0].Pools[0].Accounts[0].APIKey; got != "sk-test-from-environment" {
		t.Fatalf("resolved api key = %q", got)
	}
}

func TestValidateProviderConfigQuotaMapping(t *testing.T) {
	base := ProviderConfig{
		Name:     "minimax",
		Protocol: "openai",
		BaseURL:  "https://api.minimaxi.com",
		Models:   []ModelConfig{{Public: "minimax", Upstream: "MiniMax-M3"}},
		Pools: []PoolConfig{{
			Name:     "primary",
			Accounts: []AccountConfig{{Name: "main", APIKey: "sk-test"}},
		}},
		Quota: &ProviderQuotaConfig{
			URL:       "https://www.minimaxi.com/v1/token_plan/remains",
			ItemsPath: "model_remains",
			Fields: map[string]string{
				"model_name":                "model_name",
				"current_remaining_percent": "current_interval_remaining_percent",
			},
		},
	}

	if err := ValidateProviderConfig(base); err != nil {
		t.Fatalf("valid quota config rejected: %v", err)
	}
	if interval, err := ProviderQuotaInterval(*base.Quota); err != nil || interval != 10*time.Minute {
		t.Fatalf("default quota interval = %s, %v", interval, err)
	}

	tests := []struct {
		name   string
		mutate func(*ProviderQuotaConfig)
	}{
		{"missing url", func(quota *ProviderQuotaConfig) { quota.URL = "" }},
		{"relative url", func(quota *ProviderQuotaConfig) { quota.URL = "/quota" }},
		{"short interval", func(quota *ProviderQuotaConfig) { quota.Interval = "30s" }},
		{"invalid interval", func(quota *ProviderQuotaConfig) { quota.Interval = "often" }},
		{"missing fields", func(quota *ProviderQuotaConfig) { quota.Fields = nil }},
		{"missing model name", func(quota *ProviderQuotaConfig) { delete(quota.Fields, "model_name") }},
		{"unknown field", func(quota *ProviderQuotaConfig) { quota.Fields["balance"] = "balance" }},
		{"invalid scale", func(quota *ProviderQuotaConfig) { quota.Scales = map[string]float64{"current_remaining_percent": 0} }},
		{"invalid status", func(quota *ProviderQuotaConfig) { quota.StatusValues = map[string]string{"1": "healthy"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			quota := *base.Quota
			quota.Fields = cloneTestStringMap(base.Quota.Fields)
			quota.Scales = cloneTestFloatMap(base.Quota.Scales)
			quota.StatusValues = cloneTestStringMap(base.Quota.StatusValues)
			candidate.Quota = &quota
			test.mutate(candidate.Quota)
			if err := ValidateProviderConfig(candidate); err == nil {
				t.Fatal("expected quota validation error")
			}
		})
	}
}

func cloneTestStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneTestFloatMap(input map[string]float64) map[string]float64 {
	output := make(map[string]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
