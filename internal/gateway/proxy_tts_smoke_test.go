package gateway

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/store"
	"github.com/linlay/transit-hub/internal/usage"
	"gopkg.in/yaml.v3"
)

// Explicitly opt in: these requests incur upstream synthesis charges. Prefer
// the normal account environment variables. An optional existing Platform
// provider directory can supply credentials without copying them into files.
func TestTTSProviderSmoke(t *testing.T) {
	if os.Getenv("TRANSIT_HUB_TTS_SMOKE") != "1" {
		t.Skip("set TRANSIT_HUB_TTS_SMOKE=1 to run paid TTS provider smoke tests")
	}
	for _, tc := range []struct{ protocol, provider, host, model, path, body, keyEnv string }{
		{usage.EndpointDashScopeTTS, "dashscope", "https://dashscope.aliyuncs.com", "qwen3-tts-flash", "/bailian/v1/services/aigc/multimodal-generation/generation", `{"model":"public-model","input":{"text":"你好。","voice":"Cherry"}}`, "BAILIAN_TTS_API_KEY"},
		{usage.EndpointMiniMaxTTS, "minimax", "https://api.minimaxi.com", "speech-2.8-hd", "/minimax/v1/t2a_v2", `{"model":"public-model","text":"你好。","stream":false,"output_format":"hex","voice_setting":{"voice_id":"male-qn-qingse"},"audio_setting":{"format":"mp3","sample_rate":32000}}`, "MINIMAX_TTS_API_KEY"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			apiKey := strings.TrimSpace(os.Getenv(tc.keyEnv))
			if apiKey == "" && os.Getenv("TRANSIT_HUB_TTS_SMOKE_PROVIDER_DIR") != "" {
				data, err := os.ReadFile(filepath.Join(os.Getenv("TRANSIT_HUB_TTS_SMOKE_PROVIDER_DIR"), tc.provider+".yml"))
				if err != nil {
					t.Fatal("could not read configured smoke provider")
				}
				var provider struct {
					APIKey string `yaml:"apiKey"`
				}
				if yaml.Unmarshal(data, &provider) != nil {
					t.Fatal("could not parse configured smoke provider")
				}
				apiKey = strings.TrimSpace(provider.APIKey)
			}
			if apiKey == "" {
				t.Fatal("smoke provider credential is missing")
			}
			cfg := ttsProvider(tc.protocol)
			cfg.BaseURL = tc.host
			cfg.Pools[0].Accounts[0].APIKey = apiKey
			cfg.Models[0].Upstream = tc.model
			app, db, key := newTestGatewayWithKey(t, []config.ProviderConfig{cfg}, store.CreateAPIKeyParams{Name: "smoke", QuotaMicrocredits: 1_000_000})
			app.client = &http.Client{Timeout: 60 * time.Second}
			price := int64(10000) // local disposable test tariff, not a production price
			if _, err := db.UpsertModelPrice(t.Context(), store.ModelPriceParams{Protocol: tc.protocol, PublicModel: "public-model", Billing: &store.PriceBilling{Mode: "characters", MicrocreditsPer10KCharacters: &price}}); err != nil {
				t.Fatal("could not configure local smoke price")
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			entry := onlyLog(t, app, db)
			if rec.Code != 200 || entry.ErrorType != "" || entry.Characters <= 0 || entry.RequestTokens != 0 || entry.ResponseTokens != 0 || entry.BillingStatus != "charged" {
				t.Fatalf("smoke failed: status=%d source=%s billing=%s error=%s", rec.Code, entry.UsageSource, entry.BillingStatus, entry.ErrorType)
			}
			var response struct {
				Data struct {
					Audio string `json:"audio"`
				} `json:"data"`
				Output struct {
					Audio struct {
						URL  string `json:"url"`
						Data string `json:"data"`
					} `json:"audio"`
				} `json:"output"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &response) != nil {
				t.Fatal("invalid smoke JSON response")
			}
			var audio []byte
			var err error
			if tc.protocol == usage.EndpointMiniMaxTTS {
				audio, err = hex.DecodeString(response.Data.Audio)
			} else if response.Output.Audio.Data != "" {
				audio, err = base64.StdEncoding.DecodeString(response.Output.Audio.Data)
			} else {
				// Download independently, without a synthesis API credential.
				resp, downloadErr := app.client.Get(response.Output.Audio.URL)
				if downloadErr != nil {
					t.Fatal("could not download generated audio")
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("audio download status=%d", resp.StatusCode)
				}
				audio, err = io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
			}
			if err != nil || len(audio) < 12 {
				t.Fatal("could not decode generated audio")
			}
			wav := string(audio[:4]) == "RIFF" && string(audio[8:12]) == "WAVE"
			mp3 := string(audio[:3]) == "ID3" || (audio[0] == 0xff && audio[1]&0xe0 == 0xe0)
			if !wav && !mp3 {
				t.Fatal("generated payload is not WAV/MP3 audio")
			}
			t.Logf("protocol=%s characters=%d audio_bytes=%d charged_microcredits=%d", tc.protocol, entry.Characters, len(audio), entry.ChargedMicrocredits)
		})
	}
}
