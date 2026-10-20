package usage

import (
	"strings"
	"testing"
)

// feed writes data in small pieces so events, JSON fields and line endings split.
func feed(p *Parser, data string) {
	for start := 0; start < len(data); start += 7 {
		p.Write([]byte(data[start:min(start+7, len(data))]))
	}
}

func parse(endpoint string, mapping Mapping, stream bool, data string, complete bool) Observation {
	p := NewParser(endpoint, mapping, stream)
	feed(p, data)
	return p.Finish(complete)
}

func TestResponsesStreamUsageSnapshots(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			event := "event: " + terminal + "\ndata: {\"type\":\"" + terminal + "\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":40},\"output_tokens_details\":{\"reasoning_tokens\":10}}}}\n\n"
			data := "data: {\"type\":\"response.created\",\"response\":{\"usage\":null}}\n\n" + strings.Repeat(event, 2)
			got := parse(EndpointResponses, DefaultMapping(EndpointResponses), true, data, true)
			if !got.Final || got.Tokens.Request != 100 || got.Tokens.Response != 20 || got.Tokens.CacheHit != 40 {
				t.Fatalf("usage: %+v", got)
			}
		})
	}
}

func TestResponsesUsageWithoutTerminalEventIsNotFinal(t *testing.T) {
	data := "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	got := parse(EndpointResponses, DefaultMapping(EndpointResponses), true, data, false)
	if got.Final || got.OutputASCIIChars != 5 {
		t.Fatalf("observation: %+v", got)
	}
}

func TestChatJSONDeepSeekCacheTokens(t *testing.T) {
	got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), false, `{
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "prompt_cache_hit_tokens": 4, "prompt_cache_miss_tokens": 6}
	}`, true)
	if !got.Final || got.Tokens.Request != 10 || got.Tokens.Response != 5 || got.Tokens.CacheHit != 4 || got.Tokens.CacheMiss != 6 {
		t.Fatalf("usage: %+v", got)
	}
}

func TestChatStreamFinalUsageWinsOverRepeatedZeroUsage(t *testing.T) {
	data := `data: {"choices":[{"delta":{"reasoning_content":"想"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}
data: {"choices":[{"delta":{"content":"ok"}}],"usage":null}
data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":8}}}
data: [DONE]
`
	got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true, data, true)
	if !got.Final || got.Tokens.Request != 10 || got.Tokens.Response != 5 || got.Tokens.CacheHit != 8 {
		t.Fatalf("usage: %+v", got)
	}
	if got.OutputASCIIChars != 2 || got.OutputOtherChars != 1 {
		t.Fatalf("output chars: %+v", got)
	}
}

func TestChatStreamZeroUsageIsFinalOnlyWhenComplete(t *testing.T) {
	data := "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0}}\n\n"
	if got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true, data, false); got.Final {
		t.Fatalf("interrupted zero usage treated as final: %+v", got)
	}
	if got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true, data, true); !got.Final {
		t.Fatalf("completed zero usage not final: %+v", got)
	}
}

func TestChatStreamInterruptedCountsVisibleOutput(t *testing.T) {
	data := `data: {"choices":[{"delta":{"reasoning_content":"思考"}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}
data: {"choices":[{"delta":{"content":"hi"}}]}
`
	got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true, data, false)
	if got.Final || got.Errored || got.OutputOtherChars != 2 || got.OutputASCIIChars != 9 {
		t.Fatalf("observation: %+v", got)
	}
	if estimate := got.EstimatedOutputTokens(); estimate != 4 {
		t.Fatalf("estimate = %d", estimate)
	}
}

func TestChatStreamErrorEvent(t *testing.T) {
	got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true, "data: {\"error\":{\"message\":\"overloaded\"}}\n\n", true)
	if got.Final || !got.Errored {
		t.Fatalf("observation: %+v", got)
	}
}

func TestMappingOverrideReadsVendorFields(t *testing.T) {
	mapping := DefaultMapping(EndpointChatCompletions).Apply(MappingOverride{CacheHit: []string{"cached_tokens"}, CacheMiss: []string{}})
	got := parse(EndpointChatCompletions, mapping, false, `{"usage":{"prompt_tokens":10,"completion_tokens":2,"cached_tokens":6,"prompt_cache_miss_tokens":99}}`, true)
	if !got.Final || got.Tokens.CacheHit != 6 || got.Tokens.CacheMiss != 0 {
		t.Fatalf("usage: %+v", got)
	}
}

func TestAnthropicCacheUsageAndSplitSSE(t *testing.T) {
	data := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20,\"cache_creation_input_tokens\":30,\"output_tokens\":1}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":40}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	got := parse(EndpointAnthropicMessages, DefaultMapping(EndpointAnthropicMessages), true, data, true)
	if !got.Final || got.Tokens.Request != 60 || got.Tokens.CacheHit != 20 || got.Tokens.CacheWrite != 30 || got.Tokens.Response != 40 {
		t.Fatalf("usage %+v", got)
	}
}

func TestAnthropicInterruptedKeepsReportedInput(t *testing.T) {
	data := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20,\"output_tokens\":1}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"abc\"}}\n\n"
	got := parse(EndpointAnthropicMessages, DefaultMapping(EndpointAnthropicMessages), true, data, false)
	if got.Final || got.Tokens.Request != 30 || got.Tokens.CacheHit != 20 || got.OutputASCIIChars != 3 {
		t.Fatalf("observation %+v", got)
	}
}

func TestEmbeddingsTotalOnlyIsInput(t *testing.T) {
	got := parse(EndpointEmbeddings, DefaultMapping(EndpointEmbeddings), false, `{"data":[],"usage":{"total_tokens":42}}`, true)
	if !got.Final || got.Tokens.Request != 42 || got.Tokens.Response != 0 {
		t.Fatalf("usage %+v", got)
	}
}

func TestJSONWithoutUsageIsNotFinal(t *testing.T) {
	got := parse(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), false, `{"choices":[{"message":{"content":"你好"}}]}`, true)
	if got.Final || got.OutputOtherChars != 2 {
		t.Fatalf("observation %+v", got)
	}
}

func TestOversizedEventIsSkipped(t *testing.T) {
	data := "data: {\"padding\":\"" + strings.Repeat("x", maxEventBytes) + "\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"
	p := NewParser(EndpointChatCompletions, DefaultMapping(EndpointChatCompletions), true)
	p.Write([]byte(data))
	if got := p.Finish(true); got.Final {
		t.Fatalf("oversized event parsed: %+v", got)
	}
}
