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
