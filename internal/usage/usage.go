package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

type Tokens struct {
	Request    int64
	Response   int64
	CacheHit   int64
	CacheMiss  int64
	CacheWrite int64
	OK         bool
}

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

func ExtractFromJSON(data []byte) Tokens {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return Tokens{}
	}
	return extractFromPayload(payload)
}

func ExtractFromSSE(data []byte) Tokens {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var latest Tokens
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			continue
		}
		if tokens := extractFromPayload(payload); tokens.OK {
			if tokens.Request > 0 {
				latest.Request = tokens.Request
				latest.CacheHit = tokens.CacheHit
				latest.CacheMiss = tokens.CacheMiss
				latest.CacheWrite = tokens.CacheWrite
			}
			if tokens.Response > 0 {
				latest.Response = tokens.Response
			}
			latest.OK = true
		}
	}
	return latest
}

func extractFromPayload(payload map[string]any) Tokens {
	if response, ok := payload["response"].(map[string]any); ok {
		return extractFromPayload(response)
	}
	if message, ok := payload["message"].(map[string]any); ok {
		return extractFromPayload(message)
	}
	usageValue, ok := payload["usage"]
	if !ok || usageValue == nil {
		return Tokens{}
	}
	usageMap, ok := usageValue.(map[string]any)
	if !ok {
		return Tokens{}
	}

	cacheHit := number(usageMap["prompt_cache_hit_tokens"])
	cacheMiss := number(usageMap["prompt_cache_miss_tokens"])
	cacheWrite := number(usageMap["cache_creation_input_tokens"])
	if hit := number(usageMap["cache_read_input_tokens"]); hit > 0 {
		cacheHit = hit
	}
	if details, ok := usageMap["prompt_tokens_details"].(map[string]any); ok {
		if hit := number(details["cached_tokens"]); hit > 0 {
			cacheHit = hit
		}
	}
	if details, ok := usageMap["input_tokens_details"].(map[string]any); ok {
		if hit := number(details["cached_tokens"]); hit > 0 {
			cacheHit = hit
		}
	}
	request := number(usageMap["prompt_tokens"]) + number(usageMap["input_tokens"])
	// Anthropic input_tokens excludes cache reads/writes, OpenAI prompt_tokens includes them.
	if _, ok := usageMap["cache_read_input_tokens"]; ok {
		request += cacheHit + cacheWrite
	} else if _, ok := usageMap["cache_creation_input_tokens"]; ok {
		request += cacheWrite
	}
	response := number(usageMap["completion_tokens"]) + number(usageMap["output_tokens"])
	total := number(usageMap["total_tokens"])

	if request == 0 && cacheHit+cacheMiss > 0 {
		request = cacheHit + cacheMiss
	}
	// Explicit zero Responses usage is authoritative, unlike missing usage.
	_, hasInput := usageMap["input_tokens"].(float64)
	_, hasOutput := usageMap["output_tokens"].(float64)
	if request == 0 && response == 0 && total == 0 && cacheHit == 0 && cacheMiss == 0 && !(hasInput && hasOutput) {
		return Tokens{}
	}
	if total > 0 && request == 0 && response == 0 {
		response = total
	}
	return Tokens{
		Request:    request,
		Response:   response,
		CacheHit:   cacheHit,
		CacheMiss:  cacheMiss,
		CacheWrite: cacheWrite,
		OK:         true,
	}
}

func number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed > 1e12 {
			return 0
		}
		return int64(typed)
	case int64:
		return max(0, min(typed, 1_000_000_000_000))
	case json.Number:
		n, _ := typed.Int64()
		return n
	default:
		return 0
	}
}

// StreamCollector observes every SSE event, including usage after the response
// log sample has reached its size limit. Individual oversized events are skipped.
type StreamCollector struct {
	pending  []byte
	dropping bool
	Tokens   Tokens
}

func (c *StreamCollector) Write(data []byte) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		complete := end >= 0
		if !complete {
			end = len(data)
		}
		if !c.dropping {
			if len(c.pending)+end > 8*1024*1024 {
				c.pending = nil
				c.dropping = true
			} else {
				c.pending = append(c.pending, data[:end]...)
			}
		}
		if !complete {
			return
		}
		if !c.dropping {
			c.Tokens = mergeTokens(c.Tokens, ExtractFromSSE(c.pending))
		}
		c.pending = c.pending[:0]
		c.dropping = false
		data = data[end+1:]
	}
}
func (c *StreamCollector) Finish() Tokens {
	if !c.dropping {
		c.Tokens = mergeTokens(c.Tokens, ExtractFromSSE(c.pending))
	}
	return c.Tokens
}
func mergeTokens(a, b Tokens) Tokens {
	if !b.OK {
		return a
	}
	if b.Request > 0 {
		a.Request = b.Request
		a.CacheHit = b.CacheHit
		a.CacheMiss = b.CacheMiss
		a.CacheWrite = b.CacheWrite
	}
	if b.Response > 0 {
		a.Response = b.Response
	}
	a.OK = true
	return a
}
