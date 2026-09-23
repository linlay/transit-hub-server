package usage

import "testing"

func TestExtractFromJSONIncludesDeepSeekCacheTokens(t *testing.T) {
	tokens := ExtractFromJSON([]byte(`{
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"prompt_cache_hit_tokens": 4,
			"prompt_cache_miss_tokens": 6
		}
	}`))

	if !tokens.OK {
		t.Fatal("tokens were not extracted")
	}
	if tokens.Request != 10 || tokens.Response != 5 {
		t.Fatalf("tokens = request %d response %d", tokens.Request, tokens.Response)
	}
	if tokens.CacheHit != 4 || tokens.CacheMiss != 6 {
		t.Fatalf("cache tokens = hit %d miss %d", tokens.CacheHit, tokens.CacheMiss)
	}
}

func TestExtractFromSSEKeepsLatestDeepSeekCacheTokens(t *testing.T) {
	tokens := ExtractFromSSE([]byte(`data: {"usage":{"prompt_tokens":3,"completion_tokens":1,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":2}}
data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}}
data: [DONE]
`))

	if !tokens.OK {
		t.Fatal("tokens were not extracted")
	}
	if tokens.Request != 10 || tokens.Response != 5 {
		t.Fatalf("tokens = request %d response %d", tokens.Request, tokens.Response)
	}
	if tokens.CacheHit != 4 || tokens.CacheMiss != 6 {
		t.Fatalf("cache tokens = hit %d miss %d", tokens.CacheHit, tokens.CacheMiss)
	}
}

func TestAnthropicCacheUsageAndSplitSSE(t *testing.T) {
	var collector StreamCollector
	for _, part := range []string{
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20,\"cache_creation_input_tokens\":30,\"output_tokens\":0}}}\n",
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_", "tokens\":40}}\n\n",
	} {
		collector.Write([]byte(part))
	}
	got := collector.Finish()
	if !got.OK || got.Request != 60 || got.CacheHit != 20 || got.CacheWrite != 30 || got.Response != 40 {
		t.Fatalf("usage %+v", got)
	}
}
