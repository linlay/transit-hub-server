package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/linlay/transit-hub/internal/store"
)

type billingContextKey struct{}

func withBillingContext(r *http.Request, started time.Time) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), billingContextKey{}, started.UTC()))
}
func billingStartedAt(r *http.Request) time.Time {
	if at, ok := r.Context().Value(billingContextKey{}).(time.Time); ok {
		return at
	}
	return time.Now().UTC()
}
func (g *Gateway) beginKeyRequest(id string) bool {
	g.concurrentMu.Lock()
	defer g.concurrentMu.Unlock()
	if g.concurrent == nil {
		g.concurrent = map[string]int{}
	}
	limit := g.env.MaxConcurrentPerKey
	if limit <= 0 {
		limit = 16
	}
	if g.concurrent[id] >= limit {
		return false
	}
	g.concurrent[id]++
	return true
}
func (g *Gateway) endKeyRequest(id string) {
	g.concurrentMu.Lock()
	defer g.concurrentMu.Unlock()
	g.concurrent[id]--
	if g.concurrent[id] <= 0 {
		delete(g.concurrent, id)
	}
}

func prepareBillingRequest(body *parsedProxyBody, price *store.ModelPrice, modelType, protocol string) (int64, error) {
	if price == nil {
		return 0, nil
	}
	if modelType == "image-generation" {
		if price.Billing.Mode != "image" && price.Billing.Mode != "free" {
			return 0, errors.New("image model requires image billing prices")
		}
		params := map[string]string{}
		if body.format == proxyBodyMultipart {
			reader := multipart.NewReader(bytes.NewReader(body.Body), body.boundary)
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					return 0, err
				}
				name := part.FormName()
				if part.FileName() == "" && (name == "size" || name == "quality" || name == "n") {
					value, err := io.ReadAll(io.LimitReader(part, 1024))
					if err != nil {
						return 0, err
					}
					if _, ok := params[name]; ok {
						return 0, errors.New("duplicate image billing parameter")
					}
					params[name] = string(value)
				}
				part.Close()
			}
		} else {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body.Body, &raw); err != nil {
				return 0, err
			}
			for _, name := range []string{"size", "quality"} {
				if value, ok := raw[name]; ok {
					var text string
					if err := json.Unmarshal(value, &text); err != nil {
						return 0, err
					}
					params[name] = text
				}
			}
			if n, ok := raw["n"]; ok {
				params["n"] = string(n)
			}
		}
		if n, ok := params["n"]; ok {
			count, err := strconv.ParseInt(n, 10, 64)
			if err != nil || count < 1 || count > 10 {
				return 0, errors.New("image n must be between 1 and 10")
			}
		}
		unit, ok := price.ImageUnitCost(params["size"], params["quality"])
		if !ok {
			return 0, errors.New("no unambiguous image price for requested size and quality")
		}
		return unit, nil
	}
	if price.Billing.Mode == "image" {
		return 0, errors.New("image billing requires an image model")
	}
	if modelType == "embedding" {
		return 0, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body.Body, &raw); err != nil {
		return 0, err
	}
	// Output budgets belong to the client and upstream, not billing.
	// Ask compatible OpenAI streams for authoritative usage in the final chunk.
	if protocol == "openai" && body.Envelope.Stream {
		opts := map[string]json.RawMessage{}
		if value, ok := raw["stream_options"]; ok {
			if json.Unmarshal(value, &opts) != nil {
				return 0, errors.New("invalid stream_options")
			}
		}
		if opts == nil {
			opts = map[string]json.RawMessage{}
		}
		opts["include_usage"] = json.RawMessage("true")
		raw["stream_options"], _ = json.Marshal(opts)
	}
	var err error
	body.Body, err = json.Marshal(raw)
	return 0, err
}

func responseImageCount(sample []byte) int64 {
	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(sample, &result) != nil {
		return 0
	}
	return int64(len(result.Data))
}
func imageResponseCost(price *store.ModelPrice, unit int64, sample []byte, status int) int64 {
	if price == nil || price.Billing.Mode != "image" || status < 200 || status >= 300 {
		return 0
	}
	return store.ImageCost(responseImageCount(sample), unit)
}

func billingRequestedImageCount(body parsedProxyBody) int64 {
	if body.format == proxyBodyMultipart {
		reader := multipart.NewReader(bytes.NewReader(body.Body), body.boundary)
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "n" && part.FileName() == "" {
				value, _ := io.ReadAll(io.LimitReader(part, 1024))
				n, _ := strconv.ParseInt(string(value), 10, 64)
				return max(1, n)
			}
			part.Close()
		}
	} else {
		var raw struct {
			N int64 `json:"n"`
		}
		if json.Unmarshal(body.Body, &raw) == nil {
			return max(1, raw.N)
		}
	}
	return 1
}
