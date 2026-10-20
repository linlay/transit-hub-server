package usage

import "strings"

// Endpoint keys mirror the provider endpoint keys used for routing.
const (
	EndpointChatCompletions   = "openai_chat_completions"
	EndpointResponses         = "openai_responses"
	EndpointEmbeddings        = "openai_embeddings"
	EndpointImageGenerations  = "openai_image_generations"
	EndpointImageEdits        = "openai_image_edits"
	EndpointImageVariations   = "openai_image_variations"
	EndpointAnthropicMessages = "anthropic_messages"
)

// ProtocolEndpoints lists the endpoints whose usage a provider protocol can report.
func ProtocolEndpoints(protocol string) []string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "minimax_tts":
		return []string{EndpointMiniMaxTTS}
	case "dashscope_tts":
		return []string{EndpointDashScopeTTS}
	case "anthropic":
		return []string{EndpointAnthropicMessages}
	case "openai":
		return []string{EndpointChatCompletions, EndpointResponses, EndpointEmbeddings, EndpointImageGenerations, EndpointImageEdits, EndpointImageVariations}
	default:
		return nil
	}
}

// Mapping declares which fields of one usage object carry each billing counter.
// Each counter lists dotted paths relative to the usage object; the first path
// present in the payload wins. Where the usage object sits inside the response
// is protocol semantics and is fixed by the parser, not by the mapping.
type Mapping struct {
	Input      []string
	Output     []string
	Total      []string
	CacheHit   []string
	CacheMiss  []string
	CacheWrite []string
	// InputExcludesCache marks protocols whose input counter omits cache reads
	// and writes (Anthropic); billed input is then input + hit + write.
	InputExcludesCache bool
}

// MappingOverride replaces individual Mapping fields. Nil fields keep the base value.
type MappingOverride struct {
	Input              []string
	Output             []string
	Total              []string
	CacheHit           []string
	CacheMiss          []string
	CacheWrite         []string
	InputExcludesCache *bool
}

// DefaultMapping returns the standard usage fields of an endpoint protocol.
func DefaultMapping(endpoint string) Mapping {
	switch endpoint {
	case EndpointMiniMaxTTS, EndpointDashScopeTTS:
		return Mapping{}
	case EndpointAnthropicMessages:
		return Mapping{
			Input:              paths("input_tokens"),
			Output:             paths("output_tokens"),
			CacheHit:           paths("cache_read_input_tokens"),
			CacheWrite:         paths("cache_creation_input_tokens"),
			InputExcludesCache: true,
		}
	case EndpointResponses:
		return Mapping{
			Input:    paths("input_tokens"),
			Output:   paths("output_tokens"),
			Total:    paths("total_tokens"),
			CacheHit: paths("input_tokens_details.cached_tokens"),
		}
	case EndpointEmbeddings:
		return Mapping{
			Input: paths("prompt_tokens", "input_tokens"),
			Total: paths("total_tokens"),
		}
	case EndpointImageGenerations, EndpointImageEdits, EndpointImageVariations:
		return Mapping{
			Input:    paths("input_tokens", "prompt_tokens"),
			Output:   paths("output_tokens", "completion_tokens"),
			Total:    paths("total_tokens"),
			CacheHit: paths("input_tokens_details.cached_tokens"),
		}
	default:
		// OpenAI Chat Completions. DeepSeek's top-level cache counters are a
		// widely deployed variant and remain a fallback of the default.
		return Mapping{
			Input:     paths("prompt_tokens"),
			Output:    paths("completion_tokens"),
			Total:     paths("total_tokens"),
			CacheHit:  paths("prompt_tokens_details.cached_tokens", "prompt_cache_hit_tokens"),
			CacheMiss: paths("prompt_cache_miss_tokens"),
		}
	}
}

// Apply returns m with every field set in the override replaced.
func (m Mapping) Apply(override MappingOverride) Mapping {
	replace := func(dst *[]string, src []string) {
		if src != nil {
			*dst = append([]string(nil), src...)
		}
	}
	replace(&m.Input, override.Input)
	replace(&m.Output, override.Output)
	replace(&m.Total, override.Total)
	replace(&m.CacheHit, override.CacheHit)
	replace(&m.CacheMiss, override.CacheMiss)
	replace(&m.CacheWrite, override.CacheWrite)
	if override.InputExcludesCache != nil {
		m.InputExcludesCache = *override.InputExcludesCache
	}
	return m
}

func paths(values ...string) []string {
	return values
}

// extract reads the mapped counters from one usage object. found is false when
// none of the mapped counters is present.
func (m Mapping) extract(usageObject map[string]any, endpoint string) (Tokens, bool) {
	input, hasInput := lookupFirst(usageObject, m.Input)
	output, hasOutput := lookupFirst(usageObject, m.Output)
	total, hasTotal := lookupFirst(usageObject, m.Total)
	hit, hasHit := lookupFirst(usageObject, m.CacheHit)
	miss, hasMiss := lookupFirst(usageObject, m.CacheMiss)
	write, hasWrite := lookupFirst(usageObject, m.CacheWrite)
	if !hasInput && !hasOutput && !hasTotal && !hasHit && !hasMiss && !hasWrite {
		return Tokens{}, false
	}
	if m.InputExcludesCache {
		input += hit + write
	}
	if !hasInput && hit+miss > 0 {
		input = hit + miss
	}
	if !hasInput && !hasOutput && hasTotal {
		if endpoint == EndpointEmbeddings {
			input = total
		} else {
			output = total
		}
	}
	return Tokens{Request: input, Response: output, CacheHit: hit, CacheMiss: miss, CacheWrite: write, OK: true}, true
}

func lookupFirst(object map[string]any, candidates []string) (int64, bool) {
	for _, path := range candidates {
		if value, ok := lookup(object, path); ok {
			return value, true
		}
	}
	return 0, false
}

func lookup(object map[string]any, path string) (int64, bool) {
	var current any = object
	for _, segment := range strings.Split(path, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = next[segment]
		if !ok || current == nil {
			return 0, false
		}
	}
	return number(current)
}
