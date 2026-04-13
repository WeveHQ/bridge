package edge

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

type executor struct {
	client       *http.Client
	allowedHosts []string
}

func newExecutor(client *http.Client, allowedHosts []string) *executor {
	if client == nil {
		client = http.DefaultClient
	}

	return &executor{
		client:       client,
		allowedHosts: allowedHosts,
	}
}

func ExecuteRequest(outboundTraceID string, request wire.HttpRequest, allowedHosts []string) wire.HttpResponse {
	return newExecutor(nil, allowedHosts).Execute(outboundTraceID, request)
}

func (executor *executor) Execute(outboundTraceID string, request wire.HttpRequest) wire.HttpResponse {
	startedAt := time.Now()
	requestBody, bodyError := decodeRequestBody(request.Body)
	if bodyError != nil {
		return newErrorResponse(outboundTraceID, startedAt, 0, bodyError)
	}

	if len(executor.allowedHosts) > 0 {
		host := ""
		if parsed, err := url.Parse(request.URL); err == nil {
			host = strings.ToLower(parsed.Hostname())
		}
		if !hostAllowed(host, executor.allowedHosts) {
			return hostNotAllowedResponse(outboundTraceID, startedAt, len(requestBody), host)
		}
	}

	deadline := time.UnixMilli(int64(request.DeadlineUnixMs))
	requestContext, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(requestContext, request.Method, request.URL, bytes.NewReader(requestBody))
	if err != nil {
		return newErrorResponse(outboundTraceID, startedAt, len(requestBody), err)
	}

	for _, header := range request.Headers {
		httpRequest.Header.Add(header.Name, header.Value)
	}

	httpResponse, err := executor.client.Do(httpRequest)
	if err != nil {
		return newErrorResponse(outboundTraceID, startedAt, len(requestBody), err)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, wire.MaxBodyBytes+1))
	if err != nil {
		return newErrorResponse(outboundTraceID, startedAt, len(requestBody), err)
	}
	if len(responseBody) > wire.MaxBodyBytes {
		return newErrorResponse(outboundTraceID, startedAt, len(requestBody), errors.New("response body exceeds max size"))
	}

	return wire.HttpResponse{
		OutboundTraceID: outboundTraceID,
		Status:          uint32(httpResponse.StatusCode),
		Headers:         flattenHeaders(httpResponse.Header),
		Body:            base64.StdEncoding.EncodeToString(responseBody),
		Meta: wire.ExecutionMeta{
			StartedAtUnixMs: uint64(startedAt.UnixMilli()),
			DurationMs:      uint32(time.Since(startedAt).Milliseconds()),
			BytesOut:        uint64(len(requestBody)),
			BytesIn:         uint64(len(responseBody)),
		},
	}
}

func hostAllowed(host string, allowedHosts []string) bool {
	if host == "" {
		return false
	}
	for _, allowed := range allowedHosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func hostNotAllowedResponse(outboundTraceID string, startedAt time.Time, bytesOut int, host string) wire.HttpResponse {
	return wire.HttpResponse{
		OutboundTraceID: outboundTraceID,
		Meta: wire.ExecutionMeta{
			StartedAtUnixMs: uint64(startedAt.UnixMilli()),
			DurationMs:      uint32(time.Since(startedAt).Milliseconds()),
			BytesOut:        uint64(bytesOut),
			Error: &wire.ExecutionError{
				Kind:    wire.ErrorKindHostNotAllowed,
				Message: fmt.Sprintf("host not allowed: %s", host),
			},
		},
	}
}

func newErrorResponse(outboundTraceID string, startedAt time.Time, bytesOut int, err error) wire.HttpResponse {
	return wire.HttpResponse{
		OutboundTraceID: outboundTraceID,
		Meta: wire.ExecutionMeta{
			StartedAtUnixMs: uint64(startedAt.UnixMilli()),
			DurationMs:      uint32(time.Since(startedAt).Milliseconds()),
			BytesOut:        uint64(bytesOut),
			Error: &wire.ExecutionError{
				Kind:    mapErrorKind(err),
				Message: err.Error(),
			},
		},
	}
}

func decodeRequestBody(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}

	return base64.StdEncoding.DecodeString(value)
}

func flattenHeaders(headers http.Header) []wire.HeaderEntry {
	values := make([]wire.HeaderEntry, 0, len(headers))
	for key, headerValues := range headers {
		for _, value := range headerValues {
			values = append(values, wire.HeaderEntry{Name: key, Value: value})
		}
	}
	return values
}
