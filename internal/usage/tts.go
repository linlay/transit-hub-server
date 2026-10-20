package usage

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	EndpointDashScopeTTS = "dashscope_tts"
	EndpointMiniMaxTTS   = "minimax_tts"
	// Audio strings may exceed 100 MiB. Retain only small JSON metadata,
	// replacing oversized strings with objects in the intercepted copy, never
	// the wire body. Oversized metadata then fails its declared string type.
	ttsStringLimit   = 4096
	ttsMetadataLimit = 256 * 1024
)

func IsTTSEndpoint(endpoint string) bool {
	return endpoint == EndpointDashScopeTTS || endpoint == EndpointMiniMaxTTS
}

// ttsCollector incrementally removes large strings without allocating them.
// Escape state survives chunk boundaries, including within the audio payload.
// JSON decoding at EOF validates the retained envelope and exact field types.
type ttsCollector struct {
	body                     []byte
	text                     []byte
	inString, escaped, large bool
	invalid                  bool
}

func (c *ttsCollector) write(data []byte) {
	for _, b := range data {
		if c.invalid {
			return
		}
		if !c.inString {
			if b == '"' {
				c.inString = true
				c.text = c.text[:0]
				c.large = false
			} else {
				c.body = append(c.body, b)
			}
		} else if !c.escaped && b == '"' {
			if c.large {
				c.body = append(c.body, '{', '}')
			} else {
				c.body = append(c.body, '"')
				c.body = append(c.body, c.text...)
				c.body = append(c.body, '"')
			}
			c.inString = false
		} else {
			if b < 0x20 {
				c.invalid = true
			}
			if !c.large {
				if len(c.text) == ttsStringLimit {
					c.large = true
					c.text = c.text[:0]
				} else {
					c.text = append(c.text, b)
				}
			}
			if c.escaped {
				c.escaped = false
			} else if b == '\\' {
				c.escaped = true
			}
		}
		if len(c.body) > ttsMetadataLimit {
			c.invalid = true
			c.body = nil
		}
	}
}

func (c *ttsCollector) finish(endpoint string, complete bool) Observation {
	obs := Observation{}
	if !complete || c.invalid || c.inString {
		return obs
	}
	var response struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
		Code      string `json:"code"`
		Usage     *struct {
			Characters *int64 `json:"characters"`
		} `json:"usage"`
		ExtraInfo *struct {
			Characters *int64 `json:"usage_characters"`
		} `json:"extra_info"`
		BaseResp *struct {
			Code json.Number `json:"status_code"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(c.body, &response) != nil {
		return obs
	}
	var characters *int64
	if endpoint == EndpointMiniMaxTTS {
		obs.UpstreamRequestID = strings.TrimSpace(response.TraceID)
		if response.BaseResp != nil {
			obs.ProviderCode = response.BaseResp.Code.String()
			obs.Errored = obs.ProviderCode != "" && obs.ProviderCode != "0"
		}
		if response.ExtraInfo != nil {
			characters = response.ExtraInfo.Characters
		}
	} else {
		obs.UpstreamRequestID = strings.TrimSpace(response.RequestID)
		obs.ProviderCode = strings.TrimSpace(response.Code)
		obs.Errored = obs.ProviderCode != ""
		if response.Usage != nil {
			characters = response.Usage.Characters
		}
	}
	if !obs.Errored && characters != nil && *characters >= 0 {
		obs.Characters = *characters
		obs.Final = true
	}
	return obs
}

// TTSRetryableFailure identifies provider-side capacity/server failures. Invalid
// text/voice parameters must not evict a healthy account. No automatic retries.
func TTSRetryableFailure(endpoint, code string) bool {
	if endpoint == EndpointMiniMaxTTS {
		n, _ := strconv.Atoi(code)
		return n == 1000 || n == 1001 || n == 1002 || n == 1024 || n == 1033 || n == 1039 || n == 1041 || n == 2045
	}
	return code == "Throttling" || strings.HasPrefix(code, "Throttling.") || code == "InternalError"
}
