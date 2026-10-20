package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

// maxEventBytes bounds one buffered SSE line and the buffered JSON body.
const maxEventBytes = 8 * 1024 * 1024

type Tokens struct {
	Request    int64
	Response   int64
	CacheHit   int64
	CacheMiss  int64
	CacheWrite int64
	OK         bool
}

func (t Tokens) nonZero() bool {
	return t.Request+t.Response+t.CacheHit+t.CacheMiss+t.CacheWrite > 0
}

// EstimateTokens approximates request tokens from the raw request body size.
func EstimateTokens(data []byte) int64 {
	if len(bytes.TrimSpace(data)) == 0 {
		return 0
	}
	estimated := int64(math.Ceil(float64(len(data)) / 4.0))
	if estimated < 1 {
		return 1
	}
	return estimated
}

// Observation is what the gateway learned from intercepting one response.
type Observation struct {
	Characters        int64
	UpstreamRequestID string
	ProviderCode      string
	Tokens            Tokens
	// Final reports that Tokens hold the complete usage defined by the protocol.
	Final bool
	// Errored reports an in-band upstream error event or error body.
	Errored bool
	// Visible output text forwarded to the client, used only for estimates.
	OutputASCIIChars int64
	OutputOtherChars int64
}

// EstimatedOutputTokens approximates output tokens from forwarded text using
// DeepSeek's published ratios (about 0.3 token per ASCII character and 0.6 per
// CJK character). Hidden reasoning is not visible and therefore not counted.
func (o Observation) EstimatedOutputTokens() int64 {
	return int64(math.Ceil(0.3*float64(o.OutputASCIIChars) + 0.6*float64(o.OutputOtherChars)))
}

// Parser intercepts a decoded response copy and extracts usage according to
// the endpoint protocol. It never changes what is sent to the client.
type Parser struct {
	tts      *ttsCollector
	endpoint string
	mapping  Mapping
	stream   bool

	pending  []byte
	dropping bool

	body      []byte
	truncated bool
	decoded   int64

	sawUsage   bool
	sawNonZero bool
	final      bool
	obs        Observation
}

func NewParser(endpoint string, mapping Mapping, stream bool) *Parser {
	p := &Parser{endpoint: endpoint, mapping: mapping, stream: stream}
	if IsTTSEndpoint(endpoint) {
		p.tts = &ttsCollector{}
	}
	return p
}

// Write consumes decoded response bytes in arrival order.
func (p *Parser) Write(data []byte) {
	p.decoded += int64(len(data))
	if p.tts != nil {
		p.tts.write(data)
		return
	}
	if !p.stream {
		if p.truncated {
			return
		}
		if len(p.body)+len(data) > maxEventBytes {
			p.truncated = true
			p.body = append(p.body, data[:maxEventBytes-len(p.body)]...)
			return
		}
		p.body = append(p.body, data...)
		return
	}
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		complete := end >= 0
		if !complete {
			end = len(data)
		}
		if !p.dropping {
			if len(p.pending)+end > maxEventBytes {
				// Oversized events are skipped rather than half parsed.
				p.pending = nil
				p.dropping = true
			} else {
				p.pending = append(p.pending, data[:end]...)
			}
		}
		if !complete {
			return
		}
		if !p.dropping {
			p.line(p.pending)
		}
		p.pending = p.pending[:0]
		p.dropping = false
		data = data[end+1:]
	}
}

// Finish ends the response. complete reports that the upstream body was read
// to EOF and fully delivered to the client.
func (p *Parser) Finish(complete bool) Observation {
	if p.tts != nil {
		return p.tts.finish(p.endpoint, complete && !p.stream)
	}
	if p.stream {
		if !p.dropping && len(p.pending) > 0 {
			p.line(p.pending)
			p.pending = nil
		}
		obs := p.obs
		switch p.endpoint {
		case EndpointResponses, EndpointAnthropicMessages:
			obs.Final = p.final
		default:
			// Chat-style streams may repeat zero usage on every chunk; zeros are
			// only authoritative once the stream completed.
			obs.Final = p.sawNonZero || (p.sawUsage && complete)
		}
		return obs
	}
	obs := p.obs
	if p.truncated {
		return obs
	}
	var payload map[string]any
	if err := json.Unmarshal(p.body, &payload); err != nil {
		return obs
	}
	if payload["error"] != nil || payload["type"] == "error" {
		obs.Errored = true
	}
	usageObject, _ := payload["usage"].(map[string]any)
	if usageObject == nil {
		if response, ok := payload["response"].(map[string]any); ok {
			usageObject, _ = response["usage"].(map[string]any)
		}
	}
	if usageObject != nil {
		if tokens, found := p.mapping.extract(usageObject, p.endpoint); found {
			obs.Tokens = tokens
			obs.Final = true
		}
	}
	countJSONOutput(payload, &obs)
	return obs
}

// Body returns the buffered decoded body of a non-stream response.
func (p *Parser) Body() []byte {
	return p.body
}

// DecodedBytes returns the number of decoded bytes observed.
func (p *Parser) DecodedBytes() int64 {
	return p.decoded
}

func (p *Parser) line(raw []byte) {
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return
	}
	switch p.endpoint {
	case EndpointResponses:
		p.responsesEvent(payload)
	case EndpointAnthropicMessages:
		p.anthropicEvent(payload)
	default:
		p.chatEvent(payload)
	}
}

// chatEvent handles Chat Completions chunks; usage arrives in the final chunk
// when stream_options.include_usage is set.
func (p *Parser) chatEvent(payload map[string]any) {
	if payload["error"] != nil {
		p.obs.Errored = true
	}
	if usageObject, ok := payload["usage"].(map[string]any); ok {
		if tokens, found := p.mapping.extract(usageObject, p.endpoint); found {
			p.sawUsage = true
			if tokens.nonZero() {
				p.obs.Tokens = tokens
				p.sawNonZero = true
			} else if !p.sawNonZero {
				p.obs.Tokens = tokens
			}
		}
	}
	choices, _ := payload["choices"].([]any)
	for _, choice := range choices {
		choiceMap, _ := choice.(map[string]any)
		delta, _ := choiceMap["delta"].(map[string]any)
		countMessage(delta, &p.obs)
	}
}

// responsesEvent handles Responses API events; usage is final on terminal events.
func (p *Parser) responsesEvent(payload map[string]any) {
	eventType, _ := payload["type"].(string)
	if eventType == "error" || eventType == "response.failed" {
		p.obs.Errored = true
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if usageObject, ok := response["usage"].(map[string]any); ok {
			if tokens, found := p.mapping.extract(usageObject, p.endpoint); found {
				p.obs.Tokens = tokens
				switch eventType {
				case "response.completed", "response.incomplete", "response.failed":
					p.final = true
				}
			}
		}
	}
	if strings.HasSuffix(eventType, ".delta") {
		if delta, ok := payload["delta"].(string); ok {
			countText(delta, &p.obs)
		}
	}
}

// anthropicEvent handles Messages events: message_start carries input and cache
// counters, message_delta carries the cumulative final output.
func (p *Parser) anthropicEvent(payload map[string]any) {
	eventType, _ := payload["type"].(string)
	switch eventType {
	case "error":
		p.obs.Errored = true
	case "message_start":
		message, _ := payload["message"].(map[string]any)
		if usageObject, ok := message["usage"].(map[string]any); ok {
			if tokens, found := p.mapping.extract(usageObject, p.endpoint); found {
				p.obs.Tokens = tokens
			}
		}
	case "message_delta":
		usageObject, ok := payload["usage"].(map[string]any)
		if !ok {
			return
		}
		tokens, found := p.mapping.extract(usageObject, p.endpoint)
		if !found {
			return
		}
		p.obs.Tokens.Response = tokens.Response
		if tokens.Request > 0 {
			p.obs.Tokens.Request = tokens.Request
			p.obs.Tokens.CacheHit = tokens.CacheHit
			p.obs.Tokens.CacheMiss = tokens.CacheMiss
			p.obs.Tokens.CacheWrite = tokens.CacheWrite
		}
		p.obs.Tokens.OK = true
		p.final = true
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		for _, field := range []string{"text", "thinking", "partial_json"} {
			if text, ok := delta[field].(string); ok {
				countText(text, &p.obs)
			}
		}
	}
}

func countJSONOutput(payload map[string]any, obs *Observation) {
	choices, _ := payload["choices"].([]any)
	for _, choice := range choices {
		choiceMap, _ := choice.(map[string]any)
		message, _ := choiceMap["message"].(map[string]any)
		countMessage(message, obs)
	}
	content, _ := payload["content"].([]any)
	for _, block := range content {
		blockMap, _ := block.(map[string]any)
		for _, field := range []string{"text", "thinking"} {
			if text, ok := blockMap[field].(string); ok {
				countText(text, obs)
			}
		}
		if input, ok := blockMap["input"]; ok && input != nil {
			if encoded, err := json.Marshal(input); err == nil {
				countText(string(encoded), obs)
			}
		}
	}
}

func countMessage(message map[string]any, obs *Observation) {
	if message == nil {
		return
	}
	for _, field := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
		if text, ok := message[field].(string); ok {
			countText(text, obs)
		}
	}
	toolCalls, _ := message["tool_calls"].([]any)
	for _, call := range toolCalls {
		callMap, _ := call.(map[string]any)
		function, _ := callMap["function"].(map[string]any)
		if arguments, ok := function["arguments"].(string); ok {
			countText(arguments, obs)
		}
	}
}

func countText(text string, obs *Observation) {
	for _, r := range text {
		if r < utf8.RuneSelf {
			obs.OutputASCIIChars++
		} else {
			obs.OutputOtherChars++
		}
	}
}

func number(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed > 1e12 {
			return 0, true
		}
		return int64(typed), true
	case json.Number:
		n, err := typed.Int64()
		if err != nil || n < 0 {
			return 0, true
		}
		return n, true
	default:
		return 0, false
	}
}
