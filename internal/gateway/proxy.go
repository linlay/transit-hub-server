package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linlay/transit-hub/internal/config"
	"github.com/linlay/transit-hub/internal/provider"
	"github.com/linlay/transit-hub/internal/store"
	"github.com/linlay/transit-hub/internal/usage"
)

type usageWindowsContextKey struct{}

const responseSampleLimit = 8 * 1024 * 1024

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type copyResult struct {
	Observation usage.Observation
	// Complete reports that the upstream body reached EOF and the client received all of it.
	Complete    bool
	Undecodable bool
	DecodeErr   string
	Bytes       int64
}

// Usage sources describe where a request log's token counts came from.
const (
	usageSourceNone        = "none"        // no upstream response usage applies
	usageSourceReported    = "reported"    // complete usage parsed from the response
	usageSourceMissing     = "missing"     // the response completed without usage
	usageSourceInterrupted = "interrupted" // the response ended before usage arrived
	usageSourceUndecodable = "undecodable" // the content coding could not be decoded
)

type observedUsage struct {
	CacheWriteTokens int64
	RequestTokens    int64
	ResponseTokens   int64
	CacheHitTokens   int64
	CacheMissTokens  int64
	Estimated        bool
	Source           string
}

func (g *Gateway) proxy(protocol, endpointKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		r = withBillingContext(r, started)
		key, ok := g.authenticatePublicKey(w, r)
		if !ok {
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read request body failed")
			return
		}
		parsedBody, err := parseProxyBody(endpointKey, r.Header.Get("Content-Type"), body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		envelope := parsedBody.Envelope
		if !g.enforceAPIKeyRateLimits(w, r, key) {
			return
		}

		route, ok := g.registry.Resolve(protocol, envelope.Model)
		if !ok {
			g.logCompletedRequest(r, key, store.RequestLog{
				Protocol:    protocol,
				PublicModel: envelope.Model,
				StatusCode:  http.StatusNotFound,
				Latency:     time.Since(started),
				UsageSource: usageSourceNone,
				ErrorType:   "route_not_found",
			})
			writeError(w, http.StatusNotFound, "model route not found")
			return
		}
		if !store.APIKeyAllowsModel(key, route.PublicModel) {
			g.logCompletedRequest(r, key, store.RequestLog{
				Protocol:      protocol,
				PublicModel:   route.PublicModel,
				UpstreamModel: route.UpstreamModel,
				Provider:      route.ProviderName,
				Pool:          route.PoolName,
				StatusCode:    http.StatusForbidden,
				Latency:       time.Since(started),
				UsageSource:   usageSourceNone,
				ErrorType:     "model_not_allowed",
			})
			writeError(w, http.StatusForbidden, "model not allowed for api key")
			return
		}
		if !endpointSupportsRoute(endpointKey, route) {
			g.logCompletedRequest(r, key, store.RequestLog{
				Protocol:      protocol,
				PublicModel:   route.PublicModel,
				UpstreamModel: route.UpstreamModel,
				Provider:      route.ProviderName,
				Pool:          route.PoolName,
				StatusCode:    http.StatusBadRequest,
				Latency:       time.Since(started),
				UsageSource:   usageSourceNone,
				ErrorType:     "model_type_not_supported",
			})
			writeError(w, http.StatusBadRequest, "model type not supported for endpoint")
			return
		}
		if override, exists, err := g.store.GetRouteOverride(r.Context(), envelope.Model); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if exists {
			if route, ok = g.registry.ApplyPoolOverride(route, override); !ok {
				writeError(w, http.StatusBadRequest, "route override references missing pool")
				return
			}
		}
		modelPrice, ok := g.requestModelPrice(w, r, key, protocol, route.PublicModel)
		if !ok {
			return
		}

		if !g.beginKeyRequest(key.ID) {
			writeJSON(w, http.StatusTooManyRequests, concurrencyLimitErrorResponse{
				Error:     "api key concurrent request limit exceeded; retry after an active request completes",
				Code:      "api_key_concurrency_limit_exceeded",
				Scope:     "api_key",
				Retryable: true,
			})
			return
		}
		defer g.endKeyRequest(key.ID)
		imageUnit, err := prepareBillingRequest(&parsedBody, modelPrice, route.Type, endpointKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		account, err := route.PickAccount()
		if err != nil {
			g.logCompletedRequest(r, key, store.RequestLog{
				Protocol:      protocol,
				PublicModel:   route.PublicModel,
				UpstreamModel: route.UpstreamModel,
				Provider:      route.ProviderName,
				Pool:          route.PoolName,
				StatusCode:    http.StatusServiceUnavailable,
				Latency:       time.Since(started),
				UsageSource:   usageSourceNone,
				ErrorType:     "no_healthy_account",
				ModelPrice:    modelPrice,
			})
			writeError(w, http.StatusServiceUnavailable, "no healthy upstream account")
			return
		}

		upstreamBody, err := parsedBody.prepare(route.UpstreamModel)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		upstreamReq, err := g.buildUpstreamRequest(r, route, account, endpointKey, upstreamBody)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		if g.usage != nil {
			admittedAt := time.Now().UTC()
			bindings, err := g.usage.Admit(r.Context(), key, admittedAt)
			if err != nil {
				var violation store.RateLimitViolation
				if errors.As(err, &violation) {
					retryAfter := max(1, int(time.Until(violation.ResetsAt).Seconds()))
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
					writeError(w, http.StatusTooManyRequests, err.Error())
				} else if errors.Is(err, store.ErrQuotaExhausted) || errors.Is(err, store.ErrKeyInactive) || errors.Is(err, store.ErrKeyExpired) {
					writePublicKeyValidationError(w, err)
				} else {
					writeError(w, http.StatusServiceUnavailable, "usage admission unavailable")
				}
				return
			}
			r = withBillingContext(r, admittedAt)
			r = r.WithContext(context.WithValue(r.Context(), usageWindowsContextKey{}, bindings))
		}
		resp, err := g.client.Do(upstreamReq)
		if err != nil {
			account.Breaker.Record(false)
			g.logCompletedRequest(r, key, store.RequestLog{
				Protocol:      protocol,
				PublicModel:   route.PublicModel,
				UpstreamModel: route.UpstreamModel,
				Provider:      route.ProviderName,
				Pool:          route.PoolName,
				Account:       account.Name,
				StatusCode:    http.StatusBadGateway,
				Latency:       time.Since(started),
				UsageSource:   usageSourceNone,
				ErrorType:     "upstream_error",
				ModelPrice:    modelPrice,
			})
			writeError(w, http.StatusBadGateway, "upstream request failed")
			return
		}
		defer resp.Body.Close()

		upstreamHealthy := resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500
		account.Breaker.Record(upstreamHealthy)

		copyResponseHeaders(w.Header(), resp.Header)
		upstreamRequestID := upstreamResponseRequestID(resp.Header, route.Provider.RequestIDHeader)
		if sourceHeader := route.Provider.RequestIDHeader; sourceHeader != "" && !strings.EqualFold(sourceHeader, "X-Request-Id") {
			w.Header().Del(sourceHeader)
		}
		if upstreamRequestID != "" {
			w.Header().Set("X-Request-Id", upstreamRequestID)
		}
		w.WriteHeader(resp.StatusCode)

		stream := upstreamBody.Envelope.Stream || isEventStream(resp.Header)
		parser := usage.NewParser(endpointKey, route.UsageMapping(endpointKey), stream)
		result, copyErr := copyResponse(w, resp, parser, stream)
		if copyErr != nil {
			g.logger.Printf("copy upstream response failed: provider=%s upstream_request_id=%q error=%v", route.ProviderName, upstreamRequestID, copyErr)
		}

		observed := resolveObservedUsage(body, resp.StatusCode, result)
		if modelPrice != nil && modelPrice.Billing.Mode == "image" && observed.Source != usageSourceReported {
			// Per-image billing does not depend on token usage, so its absence is expected.
			observed.Source = usageSourceNone
		}
		if observed.Source == usageSourceMissing || observed.Source == usageSourceUndecodable {
			g.usageAlerts.report(usageAnomaly{
				Source:            observed.Source,
				Provider:          route.ProviderName,
				PublicModel:       route.PublicModel,
				Endpoint:          endpointKey,
				ContentEncoding:   resp.Header.Get("Content-Encoding"),
				UpstreamRequestID: upstreamRequestID,
				DecodeErr:         result.DecodeErr,
			})
		}
		if route.Type == config.ModelTypeEmbedding {
			if observed.RequestTokens == 0 {
				observed.RequestTokens = observed.ResponseTokens
			}
			observed.ResponseTokens = 0
		}
		imageTokenUsageUnavailable := route.Type == "image-generation" && modelPrice != nil && modelPrice.Billing.Mode == "tokens" && observed.Estimated
		// Responses may contain encrypted reasoning and repeated output snapshots.
		// Byte estimates cannot represent their token usage.
		usageUnavailable := imageTokenUsageUnavailable || (endpointKey == "openai_responses" && observed.Estimated)
		if usageUnavailable {
			observed = observedUsage{Source: observed.Source}
		}
		imageCount := responseImageCount(parser.Body())
		imageEstimated := false
		if modelPrice != nil && modelPrice.Billing.Mode == "image" && imageCount == 0 && copyErr == nil && parser.DecodedBytes() > responseSampleLimit && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			imageCount = billingRequestedImageCount(parsedBody)
			imageEstimated = true
		}
		imageCost := int64(0)
		if modelPrice != nil && modelPrice.Billing.Mode == "image" {
			imageCost = store.ImageCost(imageCount, imageUnit)
		}
		if modelPrice != nil && modelPrice.Billing.Mode == "image" {
			observed = observedUsage{Estimated: imageEstimated, Source: observed.Source}
		}
		errorType := ""
		if copyErr != nil {
			errorType = "response_copy_error"
		} else if resp.StatusCode >= 400 {
			errorType = "upstream_status"
		}
		g.logCompletedRequest(r, key, store.RequestLog{
			UpstreamRequestID:   upstreamRequestID,
			Protocol:            protocol,
			PublicModel:         route.PublicModel,
			UpstreamModel:       route.UpstreamModel,
			Provider:            route.ProviderName,
			Pool:                route.PoolName,
			Account:             account.Name,
			StatusCode:          resp.StatusCode,
			Latency:             time.Since(started),
			RequestTokens:       observed.RequestTokens,
			ResponseTokens:      observed.ResponseTokens,
			CacheHitTokens:      observed.CacheHitTokens,
			CacheMissTokens:     observed.CacheMissTokens,
			CacheWriteTokens:    observed.CacheWriteTokens,
			ImageCount:          imageCount,
			UsageUnavailable:    usageUnavailable,
			ChargedMicrocredits: imageCost,
			Estimated:           observed.Estimated,
			UsageSource:         observed.Source,
			ErrorType:           errorType,
			ModelPrice:          modelPrice,
		})
	}
}

func (g *Gateway) authenticatePublicKey(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	key, ok := g.lookupPublicKey(w, r)
	if !ok {
		return store.APIKey{}, false
	}
	if err := store.ValidateUsableKey(key, time.Now().UTC()); err != nil {
		writePublicKeyValidationError(w, err)
		return store.APIKey{}, false
	}
	return key, true
}

func (g *Gateway) authenticatePublicMetadataKey(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	key, ok := g.lookupPublicKey(w, r)
	if !ok {
		return store.APIKey{}, false
	}
	if err := validatePublicKeyActive(key, time.Now().UTC()); err != nil {
		writePublicKeyValidationError(w, err)
		return store.APIKey{}, false
	}
	return key, true
}

func (g *Gateway) lookupPublicKey(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = r.Header.Get("x-api-key")
	}
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing api key")
		return store.APIKey{}, false
	}
	key, err := g.store.FindAPIKeyByPlainText(r.Context(), token)
	if errors.Is(err, store.ErrKeyNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid api key")
		return store.APIKey{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.APIKey{}, false
	}
	return key, true
}

func validatePublicKeyActive(key store.APIKey, now time.Time) error {
	if key.Status != "active" || key.ForcedExpired {
		return store.ErrKeyInactive
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return store.ErrKeyExpired
	}
	return nil
}

func writePublicKeyValidationError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, store.ErrQuotaExhausted) {
		status = http.StatusTooManyRequests
	}
	writeError(w, status, err.Error())
}

func (g *Gateway) enforceAPIKeyRateLimits(w http.ResponseWriter, r *http.Request, key store.APIKey) bool {
	if len(key.RateLimits) == 0 {
		return true
	}
	now := time.Now().UTC()
	statuses, err := g.store.RateLimitStatuses(r.Context(), key.ID, key.RateLimits, now, g.rateLimitLocation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	violation, exhausted := store.FirstRateLimitViolation(statuses)
	if !exhausted {
		return true
	}
	retryAfter := int(time.Until(violation.ResetsAt).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeError(w, http.StatusTooManyRequests, violation.Error())
	return false
}

func (g *Gateway) requestModelPrice(w http.ResponseWriter, r *http.Request, key store.APIKey, protocol, publicModel string) (*store.ModelPrice, bool) {
	price, found, err := g.store.GetModelPrice(r.Context(), protocol, publicModel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	needsCost := key.QuotaMicrocredits > 0 || store.RateLimitsNeedCost(key.RateLimits)
	if needsCost && !found {
		writeError(w, http.StatusTooManyRequests, "cost rate limit requires a model price for "+publicModel)
		return nil, false
	}
	if found && !strings.EqualFold(price.Unit, store.CreditsUnit) {
		writeError(w, http.StatusTooManyRequests, "model price unit does not match configured unit")
		return nil, false
	}
	if found && price.Billing.Mode == "tokens" && price.InputMicrocreditsPer1MTokens == 0 && price.OutputMicrocreditsPer1MTokens == 0 && (price.InputCacheHitMicrocreditsPer1MTokens == nil || *price.InputCacheHitMicrocreditsPer1MTokens == 0) && (price.Billing.CacheWriteMicrocreditsPer1M == nil || *price.Billing.CacheWriteMicrocreditsPer1M == 0) {
		writeError(w, http.StatusServiceUnavailable, "zero-priced model requires explicit free billing")
		return nil, false
	}
	if !found {
		return nil, true
	}
	return &price, true
}

func (g *Gateway) buildUpstreamRequest(r *http.Request, route provider.Route, account *provider.Account, endpointKey string, body preparedProxyBody) (*http.Request, error) {
	upstreamURL := joinUpstreamURL(route.Provider.BaseURL, route.EndpointPath(endpointKey, r.URL.Path), r.URL.RawQuery)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body.Body))
	if err != nil {
		return nil, err
	}
	req.Header = cloneHeaders(r.Header)
	stripHopByHopHeaders(req.Header)
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-admin-token")
	req.Header.Del("Content-Length")
	if body.ContentType != "" {
		req.Header.Set("Content-Type", body.ContentType)
	}
	// Without an explicit header the transport negotiates gzip and decodes it.
	if accepted := req.Header.Get("Accept-Encoding"); accepted != "" {
		if filtered := decodableAcceptEncoding(accepted); filtered != "" {
			req.Header.Set("Accept-Encoding", filtered)
		} else {
			req.Header.Del("Accept-Encoding")
		}
	}

	applyConfiguredUpstreamHeaders(req.Header, route, account)
	req.ContentLength = int64(len(body.Body))
	return req, nil
}

func applyConfiguredUpstreamHeaders(headers http.Header, route provider.Route, account *provider.Account) {
	for key, value := range route.Provider.Headers {
		headers.Set(key, value)
	}
	for key, value := range account.Headers {
		headers.Set(key, value)
	}
	account.ApplyAuth(headers, route.Protocol)
}

func endpointSupportsRoute(endpointKey string, route provider.Route) bool {
	modelType := route.Type
	if modelType == "" {
		modelType = config.ModelTypeChat
	}
	switch endpointKey {
	case "openai_chat_completions", "openai_responses", "anthropic_messages":
		return modelType == config.ModelTypeChat
	case "openai_embeddings":
		return modelType == config.ModelTypeEmbedding
	case "openai_image_generations", "openai_image_edits", "openai_image_variations":
		return modelType == config.ModelTypeImageGeneration
	default:
		return true
	}
}

// resolveObservedUsage turns the intercepted response into billing counters.
// Upstream errors never carry usage; anything short of complete reported usage
// is estimated from the request body and the visible output text.
func resolveObservedUsage(requestBody []byte, statusCode int, result copyResult) observedUsage {
	obs := result.Observation
	if statusCode < 200 || statusCode >= 300 {
		if obs.Final {
			return observedUsageFromTokens(obs.Tokens, usageSourceReported)
		}
		return observedUsage{Source: usageSourceNone}
	}
	source := usageSourceInterrupted
	switch {
	case result.Undecodable:
		source = usageSourceUndecodable
	case obs.Final:
		return observedUsageFromTokens(obs.Tokens, usageSourceReported)
	case result.Complete && !obs.Errored:
		source = usageSourceMissing
	}
	observed := observedUsage{Estimated: true, Source: source}
	if obs.Tokens.Request > 0 {
		// Anthropic reports input and cache counters before the first token.
		observed.RequestTokens = obs.Tokens.Request
		observed.CacheHitTokens = obs.Tokens.CacheHit
		observed.CacheMissTokens = obs.Tokens.CacheMiss
		observed.CacheWriteTokens = obs.Tokens.CacheWrite
	} else {
		observed.RequestTokens = usage.EstimateTokens(requestBody)
	}
	observed.ResponseTokens = max(obs.Tokens.Response, obs.EstimatedOutputTokens())
	return observed
}

func observedUsageFromTokens(tokens usage.Tokens, source string) observedUsage {
	return observedUsage{
		RequestTokens:    tokens.Request,
		ResponseTokens:   tokens.Response,
		CacheHitTokens:   tokens.CacheHit,
		CacheMissTokens:  tokens.CacheMiss,
		CacheWriteTokens: tokens.CacheWrite,
		Source:           source,
	}
}

// copyResponse forwards the upstream body byte for byte while a decoded copy
// feeds the usage parser.
func copyResponse(w http.ResponseWriter, resp *http.Response, parser *usage.Parser, flush bool) (copyResult, error) {
	tap := newDecodingTap(resp.Header.Get("Content-Encoding"), parser.Write)
	buffer := make([]byte, 32*1024)
	var written int64
	flusher, canFlush := w.(http.Flusher)
	finish := func(complete bool, err error) (copyResult, error) {
		decodeErr := tap.Close()
		result := copyResult{
			Observation: parser.Finish(complete),
			Complete:    complete,
			Undecodable: tap.unsupported || (complete && decodeErr != nil),
			Bytes:       written,
		}
		if decodeErr != nil {
			result.DecodeErr = decodeErr.Error()
		}
		return result, err
	}

	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			tap.Write(buffer[:n])
			if _, err := w.Write(buffer[:n]); err != nil {
				return finish(false, err)
			}
			written += int64(n)
			if flush && canFlush {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return finish(true, nil)
		}
		if readErr != nil {
			return finish(false, readErr)
		}
	}
}

// An upstream ID must come from response headers, never the incoming client request.
func upstreamResponseRequestID(headers http.Header, configuredHeader string) string {
	if configuredHeader != "" {
		if value := strings.TrimSpace(headers.Get(configuredHeader)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(headers.Get("X-Request-Id"))
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if _, skip := hopByHopHeaders[canonical]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeaders(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	return dst
}

func stripHopByHopHeaders(headers http.Header) {
	connection := headers.Get("Connection")
	for key := range hopByHopHeaders {
		headers.Del(key)
	}
	if connection != "" {
		for _, key := range strings.Split(connection, ",") {
			headers.Del(strings.TrimSpace(key))
		}
	}
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func joinUpstreamURL(base *url.URL, path, rawQuery string) string {
	joined := *base
	if path == "" {
		path = "/"
	}
	if strings.HasSuffix(joined.Path, "/") && strings.HasPrefix(path, "/") {
		joined.Path = joined.Path + strings.TrimPrefix(path, "/")
	} else if !strings.HasSuffix(joined.Path, "/") && !strings.HasPrefix(path, "/") {
		joined.Path = joined.Path + "/" + path
	} else {
		joined.Path = joined.Path + path
	}
	joined.RawQuery = rawQuery
	return joined.String()
}

func isEventStream(headers http.Header) bool {
	return strings.Contains(strings.ToLower(headers.Get("Content-Type")), "text/event-stream")
}

func (g *Gateway) logCompletedRequest(r *http.Request, key store.APIKey, logEntry store.RequestLog) {
	if key.ID == "" {
		return
	}
	logEntry.APIKeyID = key.ID
	logEntry.APIKeyName = key.Name
	logEntry.KeyPrefix = key.KeyPrefix
	logEntry.CreatedAt = time.Now().UTC()
	logEntry.DeviceID, logEntry.Source = sessionHeaders(r)
	logEntry.StartedAt = billingStartedAt(r)
	logEntry.BillingStatus = "not_charged"
	if price := logEntry.ModelPrice; price != nil {
		snapshot, _ := json.Marshal(price)
		logEntry.PriceSnapshot = string(snapshot)
		// Locally rejected requests and explicit upstream failures never incur
		// estimated charges. Successful partial streams use the observed sample.
		if logEntry.StatusCode >= 200 && logEntry.StatusCode < 300 {
			switch price.Billing.Mode {
			case "free":
				logEntry.ChargedMicrocredits = 0
				logEntry.BillingStatus = "free"
			case "image":
				if logEntry.ImageCount > 0 {
					logEntry.BillingStatus = "charged"
					if logEntry.Estimated {
						logEntry.BillingStatus = "estimated"
					}
				} else {
					logEntry.BillingStatus = "unavailable"
				}
			default:
				if logEntry.UsageUnavailable {
					logEntry.ChargedMicrocredits = 0
					logEntry.BillingStatus = "unavailable"
					break
				}
				logEntry.ChargedMicrocredits = store.TokenCost(*price, logEntry.RequestTokens, logEntry.ResponseTokens, logEntry.CacheHitTokens, logEntry.CacheWriteTokens)
				logEntry.BillingStatus = "charged"
				if logEntry.Estimated {
					logEntry.BillingStatus = "estimated"
				}
			}
		} else {
			logEntry.ChargedMicrocredits = 0
		}
	} else if logEntry.StatusCode >= 200 && logEntry.StatusCode < 300 {
		logEntry.BillingStatus = "unpriced"
	}
	if g.usage != nil {
		bindings, _ := r.Context().Value(usageWindowsContextKey{}).(store.WindowBindings)
		g.usage.Record(key.ID, logEntry.RequestTokens, logEntry.ResponseTokens, logEntry.ChargedMicrocredits, logEntry.StartedAt, bindings)
	}
	if g.telemetry != nil {
		if !g.telemetry.Enqueue(logEntry) {
			g.logger.Printf("request log dropped: telemetry queue unavailable")
		}
	}
}

func sessionHeaders(r *http.Request) (string, string) {
	deviceID := strings.TrimSpace(r.Header.Get("x-device-id"))
	if deviceID == "" {
		return "", ""
	}
	source := strings.TrimSpace(r.Header.Get("x-source"))
	if source == "" {
		source = "unknown"
	}
	return deviceID, source
}
