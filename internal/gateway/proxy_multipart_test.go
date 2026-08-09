package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linlay/transit-hub/internal/config"
)

type multipartPartSpec struct {
	Name        string
	Filename    string
	ContentType string
	Headers     textproto.MIMEHeader
	Body        []byte
}

type capturedMultipartPart struct {
	Name        string
	Filename    string
	ContentType string
	Headers     textproto.MIMEHeader
	Body        []byte
}

type capturedMultipartRequest struct {
	Path          string
	Authorization string
	XAPIKey       string
	XAdminToken   string
	HopHeader     string
	MediaType     string
	Boundary      string
	ContentLength int64
	BodyLength    int
	Parts         []capturedMultipartPart
}

func TestOpenAIImageEditMultipartProxyPreservesPartsAndRewritesModel(t *testing.T) {
	clientBoundary := "client-edit-boundary"
	firstImage := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff, 0x10}
	secondImage := []byte{0xff, 0xd8, 0xff, 0x00, 0x42}
	mask := []byte{0x89, 'P', 'N', 'G', 0x7f, 0x00}
	parts := []multipartPartSpec{
		{Name: "model", Body: []byte("public-image")},
		{Name: "prompt", Body: []byte("draw a quiet station")},
		{Name: "size", Body: []byte("1024x1024")},
		{Name: "n", Body: []byte("1")},
		{Name: "response_format", Body: []byte("b64_json")},
		{Name: "quality", Body: []byte("high")},
		{Name: "vendor_option", Body: []byte("preserve-me")},
		{Name: "image[]", Filename: "source-one.png", ContentType: "image/png", Headers: textproto.MIMEHeader{"X-Part-Trace": {"first-image"}}, Body: firstImage},
		{Name: "image[]", Filename: "source-two.jpg", ContentType: "image/jpeg", Headers: textproto.MIMEHeader{"X-Part-Trace": {"second-image"}}, Body: secondImage},
		{Name: "mask", Filename: "mask.png", ContentType: "image/png", Headers: textproto.MIMEHeader{"Content-Id": {"mask-part"}}, Body: mask},
	}
	requestBody, requestContentType := buildMultipartBody(t, clientBoundary, parts)

	var captured capturedMultipartRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = captureMultipartRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer upstream.Close()

	providerConfig := imageProviderConfig(upstream.URL)
	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{providerConfig})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("x-api-key", "client-x-api-key-must-not-leak")
	req.Header.Set("x-admin-token", "client-admin-token-must-not-leak")
	req.Header.Set("Connection", "X-Client-Hop")
	req.Header.Set("X-Client-Hop", "must-not-leak")
	req.Header.Set("Content-Type", requestContentType)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}` {
		t.Fatalf("response body was not passed through: %s", rec.Body.String())
	}
	if captured.Path != "/v1/images/edits" {
		t.Fatalf("upstream path = %q", captured.Path)
	}
	if captured.Authorization != "Bearer upstream-key" {
		t.Fatalf("upstream authorization = %q", captured.Authorization)
	}
	if captured.XAPIKey != "" || captured.XAdminToken != "" || captured.HopHeader != "" {
		t.Fatalf("client headers leaked upstream: x-api-key=%q x-admin-token=%q hop=%q", captured.XAPIKey, captured.XAdminToken, captured.HopHeader)
	}
	if captured.MediaType != "multipart/form-data" || captured.Boundary == "" || captured.Boundary == clientBoundary {
		t.Fatalf("upstream content type was not rebuilt: media_type=%q boundary=%q", captured.MediaType, captured.Boundary)
	}
	if captured.ContentLength != int64(captured.BodyLength) || captured.ContentLength <= 0 {
		t.Fatalf("content length = %d, body length = %d", captured.ContentLength, captured.BodyLength)
	}

	wantOrder := []string{"model", "prompt", "size", "n", "response_format", "quality", "vendor_option", "image[]", "image[]", "mask"}
	gotOrder := make([]string, 0, len(captured.Parts))
	for _, part := range captured.Parts {
		gotOrder = append(gotOrder, part.Name)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("part order = %#v, want %#v", gotOrder, wantOrder)
	}
	assertCapturedPart(t, captured.Parts[0], multipartPartSpec{Name: "model", Body: []byte("upstream-image")})
	for index := 1; index <= 6; index++ {
		assertCapturedPart(t, captured.Parts[index], parts[index])
	}
	assertCapturedPart(t, captured.Parts[7], parts[7])
	assertCapturedPart(t, captured.Parts[8], parts[8])
	assertCapturedPart(t, captured.Parts[9], parts[9])
}

func TestOpenAIImageVariationMultipartUsesProviderEndpointOverride(t *testing.T) {
	imageBytes := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0xfe}
	body, contentType := buildMultipartBody(t, "client-variation-boundary", []multipartPartSpec{
		{Name: "model", Body: []byte("public-image")},
		{Name: "image", Filename: "variation-source.png", ContentType: "image/png", Body: imageBytes},
	})

	var captured capturedMultipartRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = captureMultipartRequest(t, r)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"dmFyaWF0aW9u"}]}`))
	}))
	defer upstream.Close()

	providerConfig := imageProviderConfig(upstream.URL)
	providerConfig.Endpoints = map[string]string{
		"openai_image_variations": "/provider/images/variations",
	}
	providerConfig.Models[0].Image = config.ImageModelConfig{EndpointPath: "/model/generations-only"}
	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{providerConfig})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/variations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if captured.Path != "/provider/images/variations" {
		t.Fatalf("upstream path = %q", captured.Path)
	}
	if len(captured.Parts) != 2 {
		t.Fatalf("parts = %#v", captured.Parts)
	}
	assertCapturedPart(t, captured.Parts[0], multipartPartSpec{Name: "model", Body: []byte("upstream-image")})
	assertCapturedPart(t, captured.Parts[1], multipartPartSpec{Name: "image", Filename: "variation-source.png", ContentType: "image/png", Body: imageBytes})
}

func TestOpenAIImageEditJSONStillRewritesOnlyModel(t *testing.T) {
	wantBody := map[string]any{
		"model":           "upstream-image",
		"images":          []any{"image-one", "image-two"},
		"mask":            map[string]any{"url": "data:image/png;base64,bWFzaw=="},
		"prompt":          "edit the station",
		"response_format": "b64_json",
		"vendor_option":   map[string]any{"mode": "strict"},
	}
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	providerConfig := imageProviderConfig(upstream.URL)
	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{providerConfig})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{
		"model":"public-image",
		"images":["image-one","image-two"],
		"mask":{"url":"data:image/png;base64,bWFzaw=="},
		"prompt":"edit the station",
		"response_format":"b64_json",
		"vendor_option":{"mode":"strict"}
	}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Fatalf("upstream JSON body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestMultipartProxyRejectsInvalidAndUnsupportedRequests(t *testing.T) {
	var upstreamHits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	openAIConfig := imageProviderConfig(upstream.URL)
	openAIConfig.Models = append(openAIConfig.Models,
		config.ModelConfig{Public: "public-model", Upstream: "upstream-model", Pool: "primary"},
		config.ModelConfig{Public: "public-embedding", Upstream: "upstream-embedding", Pool: "primary", Type: config.ModelTypeEmbedding},
	)
	anthropicConfig := anthropicProvider(upstream.URL)
	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{openAIConfig, anthropicConfig})

	missingModelBody, missingModelType := buildMultipartBody(t, "missing-model-boundary", []multipartPartSpec{{Name: "prompt", Body: []byte("missing model")}})
	emptyModelBody, emptyModelType := buildMultipartBody(t, "empty-model-boundary", []multipartPartSpec{{Name: "model", Body: []byte("  \t ")}})
	chatBody, chatType := buildMultipartBody(t, "chat-boundary", []multipartPartSpec{{Name: "model", Body: []byte("public-model")}})
	embeddingBody, embeddingType := buildMultipartBody(t, "embedding-boundary", []multipartPartSpec{{Name: "model", Body: []byte("public-embedding")}})
	generationBody, generationType := buildMultipartBody(t, "generation-boundary", []multipartPartSpec{{Name: "model", Body: []byte("public-image")}})
	messageBody, messageType := buildMultipartBody(t, "message-boundary", []multipartPartSpec{{Name: "model", Body: []byte("claude-public")}})

	tests := []struct {
		name        string
		path        string
		body        []byte
		contentType string
		wantError   string
	}{
		{name: "missing boundary", path: "/v1/images/edits", body: []byte("body"), contentType: "multipart/form-data", wantError: "invalid multipart body"},
		{name: "invalid boundary", path: "/v1/images/variations", body: []byte("body"), contentType: "multipart/form-data; boundary=", wantError: "invalid multipart body"},
		{name: "missing model", path: "/v1/images/edits", body: missingModelBody, contentType: missingModelType, wantError: "model is required"},
		{name: "empty model", path: "/v1/images/variations", body: emptyModelBody, contentType: emptyModelType, wantError: "model is required"},
		{name: "chat multipart", path: "/v1/chat/completions", body: chatBody, contentType: chatType, wantError: "invalid json body"},
		{name: "embedding multipart", path: "/v1/embeddings", body: embeddingBody, contentType: embeddingType, wantError: "invalid json body"},
		{name: "generation multipart", path: "/v1/images/generations", body: generationBody, contentType: generationType, wantError: "invalid json body"},
		{name: "anthropic multipart", path: "/v1/messages", body: messageBody, contentType: messageType, wantError: "invalid json body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := atomic.LoadInt64(&upstreamHits)
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer "+plainKey)
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var response errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != test.wantError {
				t.Fatalf("error = %q, want %q", response.Error, test.wantError)
			}
			if got := atomic.LoadInt64(&upstreamHits); got != before {
				t.Fatalf("upstream hits = %d, want %d", got, before)
			}
		})
	}
}

func TestMultipartProxyStreamEnvelopeAndSSEFlush(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "not-a-bool", want: false},
	} {
		t.Run("stream="+test.value, func(t *testing.T) {
			body, contentType := buildMultipartBody(t, "stream-envelope-"+strings.ReplaceAll(test.value, "-", ""), []multipartPartSpec{
				{Name: "model", Body: []byte("public-image")},
				{Name: "stream", Body: []byte(test.value)},
			})
			parsed, err := parseProxyBody("openai_image_edits", contentType, body)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Envelope.Stream != test.want {
				t.Fatalf("stream = %t, want %t", parsed.Envelope.Stream, test.want)
			}
		})
	}

	wantSSE := "data: {\"chunk\":\"one\"}\n\ndata: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := captureMultipartRequest(t, r)
		if len(captured.Parts) != 2 || string(captured.Parts[1].Body) != "true" {
			t.Fatalf("stream part not preserved: %#v", captured.Parts)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Upstream-Stream", "preserved")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, wantSSE)
	}))
	defer upstream.Close()

	providerConfig := imageProviderConfig(upstream.URL)
	app, _, plainKey := newTestGateway(t, []config.ProviderConfig{providerConfig})
	body, contentType := buildMultipartBody(t, "client-stream-boundary", []multipartPartSpec{
		{Name: "model", Body: []byte("public-image")},
		{Name: "stream", Body: []byte("true")},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || rec.Body.String() != wantSSE {
		t.Fatalf("SSE response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" || rec.Header().Get("X-Upstream-Stream") != "preserved" {
		t.Fatalf("SSE headers were not preserved: %#v", rec.Header())
	}
	if !rec.Flushed {
		t.Fatal("SSE response was not flushed")
	}
}

func imageProviderConfig(baseURL string) config.ProviderConfig {
	providerConfig := openAIProvider(baseURL)
	providerConfig.Models = []config.ModelConfig{{
		Public:   "public-image",
		Upstream: "upstream-image",
		Pool:     "primary",
		Type:     config.ModelTypeImageGeneration,
	}}
	return providerConfig
}

func buildMultipartBody(t *testing.T, boundary string, parts []multipartPartSpec) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(boundary); err != nil {
		t.Fatal(err)
	}
	for _, spec := range parts {
		header := cloneMIMEHeader(spec.Headers)
		if header == nil {
			header = make(textproto.MIMEHeader)
		}
		disposition := fmt.Sprintf(`form-data; name="%s"`, spec.Name)
		if spec.Filename != "" {
			disposition += fmt.Sprintf(`; filename="%s"`, spec.Filename)
		}
		header.Set("Content-Disposition", disposition)
		if spec.ContentType != "" {
			header.Set("Content-Type", spec.ContentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(spec.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func captureMultipartRequest(t *testing.T, r *http.Request) capturedMultipartRequest {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	captured := capturedMultipartRequest{
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		XAPIKey:       r.Header.Get("x-api-key"),
		XAdminToken:   r.Header.Get("x-admin-token"),
		HopHeader:     r.Header.Get("X-Client-Hop"),
		MediaType:     mediaType,
		Boundary:      params["boundary"],
		ContentLength: r.ContentLength,
		BodyLength:    len(rawBody),
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), captured.Boundary)
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		partBody, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		captured.Parts = append(captured.Parts, capturedMultipartPart{
			Name:        part.FormName(),
			Filename:    part.FileName(),
			ContentType: part.Header.Get("Content-Type"),
			Headers:     cloneMIMEHeader(part.Header),
			Body:        partBody,
		})
	}
	return captured
}

func assertCapturedPart(t *testing.T, got capturedMultipartPart, want multipartPartSpec) {
	t.Helper()
	if got.Name != want.Name || got.Filename != want.Filename || got.ContentType != want.ContentType || !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("part mismatch: got name=%q filename=%q content_type=%q body=%v; want name=%q filename=%q content_type=%q body=%v", got.Name, got.Filename, got.ContentType, got.Body, want.Name, want.Filename, want.ContentType, want.Body)
	}
	for key, values := range want.Headers {
		if !reflect.DeepEqual(got.Headers.Values(key), values) {
			t.Fatalf("part %q header %q = %#v, want %#v", want.Name, key, got.Headers.Values(key), values)
		}
	}
}
