package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProviderConfigsSkipsExamples(t *testing.T) {
	dir := t.TempDir()
	example := `
name: example
protocol: openai
base_url: https://example.invalid
models: []
pools: []
`
	if err := os.WriteFile(filepath.Join(dir, "deepseek.example.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}

	realConfig := `
name: deepseek
protocol: openai
base_url: https://api.deepseek.com
default_pool: primary
models:
  - public: deepseek-chat
    upstream: deepseek-chat
pools:
  - name: primary
    accounts:
      - name: main
        api_key: sk-test
        weight: 1
`
	if err := os.WriteFile(filepath.Join(dir, "deepseek.yaml"), []byte(realConfig), 0o644); err != nil {
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
}
