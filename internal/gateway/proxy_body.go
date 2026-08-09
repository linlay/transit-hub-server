package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

type requestEnvelope struct {
	Model  string
	Stream bool
}

type proxyBodyFormat uint8

const (
	proxyBodyJSON proxyBodyFormat = iota
	proxyBodyMultipart
)

type parsedProxyBody struct {
	Envelope    requestEnvelope
	Body        []byte
	ContentType string
	format      proxyBodyFormat
	boundary    string
}

type preparedProxyBody struct {
	Envelope    requestEnvelope
	Body        []byte
	ContentType string
}

func parseProxyBody(endpointKey, contentType string, body []byte) (parsedProxyBody, error) {
	mediaType, params, mediaTypeErr := mime.ParseMediaType(contentType)
	if mediaTypeErr != nil {
		if hasMultipartFormDataType(contentType) {
			return parsedProxyBody{}, fmt.Errorf("invalid multipart body")
		}
		return parseJSONProxyBody(contentType, body)
	}
	if !strings.EqualFold(mediaType, "multipart/form-data") {
		return parseJSONProxyBody(contentType, body)
	}
	if !endpointSupportsMultipart(endpointKey) {
		return parsedProxyBody{}, fmt.Errorf("invalid json body")
	}

	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return parsedProxyBody{}, fmt.Errorf("invalid multipart body")
	}
	envelope, err := parseMultipartEnvelope(body, boundary)
	if err != nil {
		return parsedProxyBody{}, err
	}
	return parsedProxyBody{
		Envelope:    envelope,
		Body:        body,
		ContentType: contentType,
		format:      proxyBodyMultipart,
		boundary:    boundary,
	}, nil
}

func parseJSONProxyBody(contentType string, body []byte) (parsedProxyBody, error) {
	envelope, err := parseRequestEnvelope(body)
	if err != nil {
		return parsedProxyBody{}, err
	}
	return parsedProxyBody{
		Envelope:    envelope,
		Body:        body,
		ContentType: contentType,
		format:      proxyBodyJSON,
	}, nil
}

func (body parsedProxyBody) prepare(upstreamModel string) (preparedProxyBody, error) {
	if body.format == proxyBodyMultipart {
		rewrittenBody, contentType, err := rewriteMultipartModel(body.Body, body.boundary, upstreamModel)
		if err != nil {
			return preparedProxyBody{}, err
		}
		return preparedProxyBody{
			Envelope:    body.Envelope,
			Body:        rewrittenBody,
			ContentType: contentType,
		}, nil
	}

	rewrittenBody, err := rewriteModel(body.Body, upstreamModel)
	if err != nil {
		return preparedProxyBody{}, err
	}
	return preparedProxyBody{
		Envelope:    body.Envelope,
		Body:        rewrittenBody,
		ContentType: body.ContentType,
	}, nil
}

func hasMultipartFormDataType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "multipart/form-data")
}

func endpointSupportsMultipart(endpointKey string) bool {
	return endpointKey == "openai_image_edits" || endpointKey == "openai_image_variations"
}

func parseMultipartEnvelope(body []byte, boundary string) (requestEnvelope, error) {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var envelope requestEnvelope
	modelFound := false
	streamFound := false

	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return requestEnvelope{}, fmt.Errorf("invalid multipart body")
		}

		isValue := isFormValuePart(part)
		switch {
		case isValue && part.FormName() == "model" && !modelFound:
			value, err := io.ReadAll(part)
			if err != nil {
				return requestEnvelope{}, fmt.Errorf("invalid multipart body")
			}
			envelope.Model = string(value)
			modelFound = true
		case isValue && part.FormName() == "stream" && !streamFound:
			value, err := io.ReadAll(part)
			if err != nil {
				return requestEnvelope{}, fmt.Errorf("invalid multipart body")
			}
			envelope.Stream = strings.EqualFold(strings.TrimSpace(string(value)), "true")
			streamFound = true
		default:
			if _, err := io.Copy(io.Discard, part); err != nil {
				return requestEnvelope{}, fmt.Errorf("invalid multipart body")
			}
		}
	}

	if !modelFound || strings.TrimSpace(envelope.Model) == "" {
		return requestEnvelope{}, fmt.Errorf("model is required")
	}
	return envelope, nil
}

func rewriteMultipartModel(body []byte, boundary, upstreamModel string) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)

	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("invalid multipart body")
		}

		target, err := writer.CreatePart(cloneMIMEHeader(part.Header))
		if err != nil {
			return nil, "", fmt.Errorf("rebuild multipart body failed")
		}
		if isFormValuePart(part) && part.FormName() == "model" {
			if _, err := io.WriteString(target, upstreamModel); err != nil {
				return nil, "", fmt.Errorf("rebuild multipart body failed")
			}
			if _, err := io.Copy(io.Discard, part); err != nil {
				return nil, "", fmt.Errorf("invalid multipart body")
			}
			continue
		}
		if _, err := io.Copy(target, part); err != nil {
			return nil, "", fmt.Errorf("rebuild multipart body failed")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("rebuild multipart body failed")
	}
	return rewritten.Bytes(), writer.FormDataContentType(), nil
}

func isFormValuePart(part *multipart.Part) bool {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return false
	}
	_, hasFilename := params["filename"]
	return !hasFilename
}

func cloneMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func parseRequestEnvelope(body []byte) (requestEnvelope, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return requestEnvelope{}, fmt.Errorf("invalid json body")
	}
	var model string
	if err := json.Unmarshal(payload["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return requestEnvelope{}, fmt.Errorf("model is required")
	}
	var stream bool
	if raw, ok := payload["stream"]; ok {
		_ = json.Unmarshal(raw, &stream)
	}
	return requestEnvelope{Model: model, Stream: stream}, nil
}

func rewriteModel(body []byte, upstreamModel string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	rawModel, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = rawModel
	return json.Marshal(payload)
}
