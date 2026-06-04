package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/provider"
)

const defaultPlaygroundMaxTokens = 1024

type playgroundChatRequest struct {
	Provider    string                  `json:"provider"`
	PublicModel string                  `json:"public_model"`
	Pool        string                  `json:"pool"`
	Account     string                  `json:"account"`
	Messages    []playgroundChatMessage `json:"messages"`
	Temperature *float64                `json:"temperature"`
	MaxTokens   int                     `json:"max_tokens"`
}

type playgroundChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type playgroundMetaEvent struct {
	Provider      string `json:"provider"`
	Protocol      string `json:"protocol"`
	PublicModel   string `json:"public_model"`
	UpstreamModel string `json:"upstream_model"`
	Pool          string `json:"pool"`
	Account       string `json:"account"`
	Endpoint      string `json:"endpoint"`
}

type playgroundDeltaEvent struct {
	Content string `json:"content"`
}

type playgroundErrorEvent struct {
	Error      string `json:"error"`
	StatusCode int    `json:"status_code,omitempty"`
}

type playgroundDoneEvent struct {
	StatusCode int   `json:"status_code"`
	LatencyMS  int64 `json:"latency_ms"`
}

type upstreamSSEEvent struct {
	Event string
	Data  string
}

func (g *Gateway) playgroundChat(w http.ResponseWriter, r *http.Request) {
	var req playgroundChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.PublicModel = strings.TrimSpace(req.PublicModel)
	req.Pool = strings.TrimSpace(req.Pool)
	req.Account = strings.TrimSpace(req.Account)
	if err := validatePlaygroundChatRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	route, account, err := g.registry.ResolveConnectivityTarget(provider.ConnectivityTarget{
		ProviderName: req.Provider,
		PublicModel:  req.PublicModel,
		PoolName:     req.Pool,
		AccountName:  req.Account,
	})
	if err != nil {
		writeConnectivityTargetError(w, err)
		return
	}

	body, endpointKey, fallbackPath, err := playgroundRequestBody(route, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	upstreamReq, err := g.buildConnectivityRequest(r, route, account, endpointKey, fallbackPath, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	upstreamReq.Header.Set("Accept", "text/event-stream")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	started := time.Now()
	_ = writePlaygroundEvent(w, "meta", playgroundMetaEvent{
		Provider:      route.ProviderName,
		Protocol:      route.Protocol,
		PublicModel:   route.PublicModel,
		UpstreamModel: route.UpstreamModel,
		Pool:          route.PoolName,
		Account:       account.Name,
		Endpoint:      upstreamReq.URL.String(),
	})

	resp, err := g.client.Do(upstreamReq)
	if err != nil {
		_ = writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: fmt.Sprintf("upstream request failed: %v", err)})
		_ = writePlaygroundEvent(w, "done", playgroundDoneEvent{LatencyMS: time.Since(started).Milliseconds()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		sample, readErr := io.ReadAll(io.LimitReader(resp.Body, connectivityErrorSampleLimit))
		message := http.StatusText(resp.StatusCode)
		if readErr != nil {
			message = readErr.Error()
		} else if len(sample) > 0 {
			message = summarizeConnectivityError(resp.StatusCode, sample)
		}
		_ = writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: message, StatusCode: resp.StatusCode})
		_ = writePlaygroundEvent(w, "done", playgroundDoneEvent{StatusCode: resp.StatusCode, LatencyMS: time.Since(started).Milliseconds()})
		return
	}

	if isEventStream(resp.Header) {
		if err := forwardPlaygroundStream(w, route.Protocol, resp.Body); err != nil {
			_ = writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: err.Error(), StatusCode: resp.StatusCode})
		}
	} else if err := forwardPlaygroundJSON(w, route.Protocol, resp.Body); err != nil {
		_ = writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: err.Error(), StatusCode: resp.StatusCode})
	}
	_ = writePlaygroundEvent(w, "done", playgroundDoneEvent{StatusCode: resp.StatusCode, LatencyMS: time.Since(started).Milliseconds()})
}

func validatePlaygroundChatRequest(req playgroundChatRequest) error {
	if req.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if req.PublicModel == "" {
		return fmt.Errorf("public_model is required")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages are required")
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	for _, message := range req.Messages {
		role := strings.TrimSpace(message.Role)
		if role != "system" && role != "user" && role != "assistant" {
			return fmt.Errorf("message role must be system, user, or assistant")
		}
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Errorf("message content is required")
		}
	}
	return nil
}

func playgroundRequestBody(route provider.Route, req playgroundChatRequest) ([]byte, string, string, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultPlaygroundMaxTokens
	}
	switch route.Protocol {
	case "openai":
		messages := make([]map[string]string, 0, len(req.Messages))
		for _, message := range req.Messages {
			messages = append(messages, map[string]string{
				"role":    strings.TrimSpace(message.Role),
				"content": message.Content,
			})
		}
		body := map[string]any{
			"model":      route.UpstreamModel,
			"messages":   messages,
			"max_tokens": maxTokens,
			"stream":     true,
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		data, err := json.Marshal(body)
		return data, "openai_chat_completions", "/v1/chat/completions", err
	case "anthropic":
		messages := make([]map[string]string, 0, len(req.Messages))
		systemParts := make([]string, 0)
		for _, message := range req.Messages {
			role := strings.TrimSpace(message.Role)
			if role == "system" {
				systemParts = append(systemParts, message.Content)
				continue
			}
			messages = append(messages, map[string]string{
				"role":    role,
				"content": message.Content,
			})
		}
		if len(messages) == 0 {
			return nil, "", "", fmt.Errorf("anthropic playground messages require at least one user or assistant message")
		}
		body := map[string]any{
			"model":      route.UpstreamModel,
			"messages":   messages,
			"max_tokens": maxTokens,
			"stream":     true,
		}
		if len(systemParts) > 0 {
			body["system"] = strings.Join(systemParts, "\n\n")
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		data, err := json.Marshal(body)
		return data, "anthropic_messages", "/v1/messages", err
	default:
		return nil, "", "", fmt.Errorf("unsupported provider protocol %q", route.Protocol)
	}
}

func forwardPlaygroundStream(w http.ResponseWriter, protocol string, body io.Reader) error {
	return readUpstreamSSE(body, func(event upstreamSSEEvent) error {
		data := strings.TrimSpace(event.Data)
		if data == "" || data == "[DONE]" {
			return nil
		}
		switch protocol {
		case "openai":
			return forwardOpenAIPlaygroundEvent(w, data)
		case "anthropic":
			return forwardAnthropicPlaygroundEvent(w, data)
		default:
			return fmt.Errorf("unsupported provider protocol %q", protocol)
		}
	})
}

func forwardOpenAIPlaygroundEvent(w http.ResponseWriter, data string) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return err
	}
	if message := errorMessageFromValue(payload["error"]); message != "" {
		return writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: message})
	}
	for _, choice := range arrayFromValue(payload["choices"]) {
		choiceMap, _ := choice.(map[string]any)
		if content := nestedString(choiceMap, "delta", "content"); content != "" {
			if err := writePlaygroundEvent(w, "delta", playgroundDeltaEvent{Content: content}); err != nil {
				return err
			}
		}
	}
	return nil
}

func forwardAnthropicPlaygroundEvent(w http.ResponseWriter, data string) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return err
	}
	if message := errorMessageFromValue(payload["error"]); message != "" {
		return writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: message})
	}
	if payload["type"] == "error" {
		if message := errorMessageFromValue(payload["error"]); message != "" {
			return writePlaygroundEvent(w, "error", playgroundErrorEvent{Error: message})
		}
	}
	if content := nestedString(payload, "delta", "text"); content != "" {
		return writePlaygroundEvent(w, "delta", playgroundDeltaEvent{Content: content})
	}
	return nil
}

func forwardPlaygroundJSON(w http.ResponseWriter, protocol string, body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, responseSampleLimit))
	if err != nil {
		return err
	}
	content := playgroundContentFromJSON(protocol, data)
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("empty upstream response")
	}
	return writePlaygroundEvent(w, "delta", playgroundDeltaEvent{Content: content})
}

func playgroundContentFromJSON(protocol string, data []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	switch protocol {
	case "openai":
		for _, choice := range arrayFromValue(payload["choices"]) {
			choiceMap, _ := choice.(map[string]any)
			if content := nestedString(choiceMap, "message", "content"); content != "" {
				return content
			}
			if content := nestedString(choiceMap, "delta", "content"); content != "" {
				return content
			}
		}
	case "anthropic":
		var out strings.Builder
		for _, block := range arrayFromValue(payload["content"]) {
			blockMap, _ := block.(map[string]any)
			if text, _ := blockMap["text"].(string); text != "" {
				out.WriteString(text)
			}
		}
		return out.String()
	}
	return ""
}

func readUpstreamSSE(body io.Reader, handle func(upstreamSSEEvent) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), responseSampleLimit)
	eventName := ""
	dataLines := make([]string, 0)

	dispatch := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		event := upstreamSSEEvent{Event: eventName, Data: strings.Join(dataLines, "\n")}
		eventName = ""
		dataLines = dataLines[:0]
		return handle(event)
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

func writePlaygroundEvent(w http.ResponseWriter, event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	_, _ = fmt.Fprintf(&buffer, "event: %s\n", event)
	_, _ = fmt.Fprintf(&buffer, "data: %s\n\n", data)
	if _, err := w.Write(buffer.Bytes()); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func arrayFromValue(value any) []any {
	items, _ := value.([]any)
	return items
}

func nestedString(payload map[string]any, path ...string) string {
	var current any = payload
	for _, key := range path {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = currentMap[key]
	}
	text, _ := current.(string)
	return text
}

func errorMessageFromValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if message, _ := typed["message"].(string); strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
		if detail, _ := typed["detail"].(string); strings.TrimSpace(detail) != "" {
			return strings.TrimSpace(detail)
		}
	}
	return ""
}
