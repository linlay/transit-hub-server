package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

type Tokens struct {
	Request   int64
	Response  int64
	CacheHit  int64
	CacheMiss int64
	OK        bool
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
			latest = tokens
		}
	}
	return latest
}

func extractFromPayload(payload map[string]any) Tokens {
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
	request := number(usageMap["prompt_tokens"]) + number(usageMap["input_tokens"])
	response := number(usageMap["completion_tokens"]) + number(usageMap["output_tokens"])
	total := number(usageMap["total_tokens"])

	if request == 0 && cacheHit+cacheMiss > 0 {
		request = cacheHit + cacheMiss
	}
	if request == 0 && response == 0 && total == 0 && cacheHit == 0 && cacheMiss == 0 {
		return Tokens{}
	}
	if total > 0 && request == 0 && response == 0 {
		response = total
	}
	return Tokens{
		Request:   request,
		Response:  response,
		CacheHit:  cacheHit,
		CacheMiss: cacheMiss,
		OK:        true,
	}
}

func number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		n, _ := typed.Int64()
		return n
	default:
		return 0
	}
}
