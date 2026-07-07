package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if configs[0].Models[0].Type != ModelTypeEmbedding {
		t.Fatalf("embedding model type = %q", configs[0].Models[0].Type)
	}
	if got := configs[0].Models[1].Image.EndpointPathValue(); got != "/v1/images/generations" {
		t.Fatalf("image endpoint path = %q", got)
	}
}
