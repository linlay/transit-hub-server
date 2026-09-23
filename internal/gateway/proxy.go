package gateway

import (
	"bytes"
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
	Usage  usage.Tokens
	Sample []byte
	Bytes  int64
}

type observedUsage struct {
	CacheWriteTokens int64
	RequestTokens    int64
	ResponseTokens   int64
	CacheHitTokens   int64
	CacheMissTokens  int64
	Estimated        bool
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
				Protocol:      protocol,
				PublicModel:   envelope.Model,
				StatusCode:    http.StatusNotFound,
				Latency:       time.Since(started),
				RequestTokens: usage.EstimateTokens(body),
				Estimated:     true,
				ErrorType:     "route_not_found",
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
				RequestTokens: usage.EstimateTokens(body),
				Estimated:     true,
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
				RequestTokens: usage.EstimateTokens(body),
				Estimated:     true,
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
			writeError(w, http.StatusTooManyRequests, "api key concurrent request limit exhausted")
			return
		}
		defer g.endKeyRequest(key.ID)
		imageUnit, err := prepareBillingRequest(&parsedBody, modelPrice, route.Type, protocol)
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
				RequestTokens: usage.EstimateTokens(body),
				Estimated:     true,
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
				RequestTokens: usage.EstimateTokens(body),
				Estimated:     true,
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
		w.WriteHeader(resp.StatusCode)

		result, copyErr := copyResponse(w, resp.Body, upstreamBody.Envelope.Stream || isEventStream(resp.Header))
		if copyErr != nil {
			g.logger.Printf("copy upstream response failed: %v", copyErr)
		}

		observed := observedTokens(body, result.Sample, upstreamBody.Envelope.Stream || isEventStream(resp.Header))
		if result.Usage.OK {
			observed = observedUsage{RequestTokens: result.Usage.Request, ResponseTokens: result.Usage.Response, CacheHitTokens: result.Usage.CacheHit, CacheMissTokens: result.Usage.CacheMiss, CacheWriteTokens: result.Usage.CacheWrite}
		}
		if copyErr != nil && observed.ResponseTokens == 0 && result.Bytes > 0 {
			observed.ResponseTokens = usage.EstimateTokens(result.Sample)
			observed.Estimated = true
		}
		if route.Type == config.ModelTypeEmbedding {
			if observed.RequestTokens == 0 {
				observed.RequestTokens = observed.ResponseTokens
			}
			observed.ResponseTokens = 0
		}
		imageCount := responseImageCount(result.Sample)
		imageEstimated := false
		if modelPrice != nil && modelPrice.Billing.Mode == "image" && imageCount == 0 && copyErr == nil && result.Bytes > responseSampleLimit && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			imageCount = billingRequestedImageCount(parsedBody)
			imageEstimated = true
		}
		imageCost := int64(0)
		if modelPrice != nil && modelPrice.Billing.Mode == "image" {
			imageCost = store.ImageCost(imageCount, imageUnit)
		}
		if modelPrice != nil && modelPrice.Billing.Mode == "image" {
			observed = observedUsage{Estimated: imageEstimated}
		}
		errorType := ""
		if copyErr != nil {
			errorType = "response_copy_error"
		} else if !upstreamHealthy {
			errorType = "upstream_status"
		}
		g.logCompletedRequest(r, key, store.RequestLog{
			Protocol:         protocol,
			PublicModel:      route.PublicModel,
			UpstreamModel:    route.UpstreamModel,
			Provider:         route.ProviderName,
			Pool:             route.PoolName,
			Account:          account.Name,
			StatusCode:       resp.StatusCode,
			Latency:          time.Since(started),
			RequestTokens:    observed.RequestTokens,
			ResponseTokens:   observed.ResponseTokens,
			CacheHitTokens:   observed.CacheHitTokens,
			CacheMissTokens:  observed.CacheMissTokens,
			CacheWriteTokens: observed.CacheWriteTokens,
			ImageCount:       imageCount,
			CostMicro:        imageCost,
			Estimated:        observed.Estimated,
			ErrorType:        errorType,
			ModelPrice:       modelPrice,
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
	needsCost := key.CostQuotaMicro > 0 || store.RateLimitsNeedCost(key.RateLimits)
	if needsCost && !found {
		writeError(w, http.StatusTooManyRequests, "cost rate limit requires a model price for "+publicModel)
		return nil, false
	}
	if found && (!strings.EqualFold(price.Currency, g.configuredCurrency()) || !strings.EqualFold(price.Currency, "CNY")) {
		writeError(w, http.StatusTooManyRequests, "model price currency does not match configured currency")
		return nil, false
	}
	if found && price.Billing.Mode == "tokens" && price.InputCostMicroPer1MTokens == 0 && price.OutputCostMicroPer1MTokens == 0 && (price.InputCacheHitCostMicroPer1MTokens == nil || *price.InputCacheHitCostMicroPer1MTokens == 0) && (price.Billing.CacheWriteCostMicroPer1M == nil || *price.Billing.CacheWriteCostMicroPer1M == 0) {
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
	case "openai_chat_completions", "anthropic_messages":
		return modelType == config.ModelTypeChat
	case "openai_embeddings":
		return modelType == config.ModelTypeEmbedding
	case "openai_image_generations", "openai_image_edits", "openai_image_variations":
		return modelType == config.ModelTypeImageGeneration
	default:
		return true
	}
}

func observedTokens(requestBody, responseSample []byte, stream bool) observedUsage {
	var observed usage.Tokens
	if stream {
		observed = usage.ExtractFromSSE(responseSample)
	} else {
		observed = usage.ExtractFromJSON(responseSample)
	}
	if observed.OK {
		return observedUsage{
			RequestTokens:    observed.Request,
			ResponseTokens:   observed.Response,
			CacheHitTokens:   observed.CacheHit,
			CacheMissTokens:  observed.CacheMiss,
			CacheWriteTokens: observed.CacheWrite,
			Estimated:        false,
		}
	}
	return observedUsage{
		RequestTokens:  usage.EstimateTokens(requestBody),
		ResponseTokens: usage.EstimateTokens(responseSample),
		Estimated:      true,
	}
}

func copyResponse(w http.ResponseWriter, body io.Reader, flush bool) (copyResult, error) {
	var sample bytes.Buffer
	var collector usage.StreamCollector
	buffer := make([]byte, 32*1024)
	var written int64
	flusher, canFlush := w.(http.Flusher)

	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if flush {
				collector.Write(chunk)
			}
			if sample.Len() < responseSampleLimit {
				remaining := responseSampleLimit - sample.Len()
				if len(chunk) > remaining {
					chunk = chunk[:remaining]
				}
				_, _ = sample.Write(chunk)
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				return copyResult{Sample: sample.Bytes(), Bytes: written, Usage: collector.Finish()}, err
			}
			written += int64(n)
			if flush && canFlush {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return copyResult{Sample: sample.Bytes(), Bytes: written, Usage: collector.Finish()}, nil
		}
		if readErr != nil {
			return copyResult{Sample: sample.Bytes(), Bytes: written, Usage: collector.Finish()}, readErr
		}
	}
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
				logEntry.CostMicro = 0
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
				logEntry.CostMicro = store.TokenCost(*price, logEntry.RequestTokens, logEntry.ResponseTokens, logEntry.CacheHitTokens, logEntry.CacheWriteTokens)
				logEntry.BillingStatus = "charged"
				if logEntry.Estimated {
					logEntry.BillingStatus = "estimated"
				}
			}
		} else {
			logEntry.CostMicro = 0
		}
	} else if logEntry.StatusCode >= 200 && logEntry.StatusCode < 300 {
		logEntry.BillingStatus = "unpriced"
	}
	if g.usage != nil {
		g.usage.Record(key.ID, logEntry.RequestTokens, logEntry.ResponseTokens, logEntry.CostMicro, logEntry.StartedAt)
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
