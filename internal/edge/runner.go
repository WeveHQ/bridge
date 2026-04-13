package edge

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/wire"
)

const retryInterval = 10 * time.Second

type Config struct {
	Token             string
	HubURL            string
	PollConcurrency   int
	HeartbeatInterval time.Duration
	PollTimeout       time.Duration
	Client            *http.Client
}

type Runner struct {
	client            *http.Client
	token             string
	hubURL            string
	pollConcurrency   int
	heartbeatInterval time.Duration
	pollTimeout       time.Duration
	startedAt         time.Time
	inFlight          atomic.Int32
}

func NewRunner(cfg Config) *Runner {
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}

	return &Runner{
		client:            client,
		token:             cfg.Token,
		hubURL:            strings.TrimRight(cfg.HubURL, "/"),
		pollConcurrency:   cfg.PollConcurrency,
		heartbeatInterval: cfg.HeartbeatInterval,
		pollTimeout:       cfg.PollTimeout,
		startedAt:         time.Now(),
	}
}

func (runner *Runner) Run(ctx context.Context) error {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, runner.pollConcurrency+1)
	go runner.runHeartbeatLoop(heartbeatCtx, errCh)

	for index := 0; index < runner.pollConcurrency; index++ {
		go runner.runPollSlot(ctx, errCh)
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if err == nil {
			return nil
		}
		return err
	}
}

func (runner *Runner) runHeartbeatLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(runner.heartbeatInterval)
	defer ticker.Stop()

	for {
		if err := runner.sendHeartbeat(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "weve-bridge: heartbeat failed: %v, retrying in %s\n", err, retryInterval)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (runner *Runner) runPollSlot(ctx context.Context, errCh chan<- error) {
	for {
		dispatch, ok, err := runner.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			fmt.Fprintf(os.Stderr, "weve-bridge: poll failed: %v, retrying in %s\n", err, retryInterval)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}
		if !ok {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}

		go runner.runPollSlot(ctx, errCh)
		runner.inFlight.Add(1)
		_ = runner.handleDispatch(ctx, dispatch)
		runner.inFlight.Add(-1)
		return
	}
}

func (runner *Runner) handleDispatch(ctx context.Context, dispatch wire.PollResponse) error {
	response := ExecuteRequest(dispatch.OutboundTraceID, dispatch.Req)
	return runner.postResponse(ctx, response)
}

func (runner *Runner) poll(ctx context.Context) (wire.PollResponse, bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, runner.pollTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, runner.hubURL+wire.PollPath, bytes.NewReader(wire.MustJSON(wire.PollRequest{
		BridgeVersion: build.Version,
	})))
	if err != nil {
		return wire.PollResponse{}, false, err
	}
	runner.decorateAuth(request)

	response, err := runner.client.Do(request)
	if err != nil {
		return wire.PollResponse{}, false, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNoContent {
		return wire.PollResponse{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return wire.PollResponse{}, false, errors.New(string(body))
	}

	var parsed wire.PollResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return wire.PollResponse{}, false, err
	}

	return parsed, true, nil
}

func (runner *Runner) sendHeartbeat(ctx context.Context) error {
	hostname, _ := os.Hostname()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.hubURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
		BridgeVersion: build.Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		UptimeSec:     uint64(time.Since(runner.startedAt).Seconds()),
		InFlight:      uint32(runner.inFlight.Load()),
		Hostname:      hostname,
	})))
	if err != nil {
		return err
	}
	runner.decorateAuth(request)

	response, err := runner.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return errors.New(string(body))
	}

	return nil
}

func (runner *Runner) postResponse(ctx context.Context, response wire.HttpResponse) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.hubURL+wire.ResponsePathPrefix+response.OutboundTraceID, bytes.NewReader(wire.MustJSON(response)))
	if err != nil {
		return err
	}
	runner.decorateAuth(request)

	httpResponse, err := runner.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResponse.Body)
		return errors.New(string(body))
	}

	return nil
}

func (runner *Runner) decorateAuth(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+runner.token)
	request.Header.Set("Content-Type", "application/json")
}

func ExecuteRequest(outboundTraceID string, request wire.HttpRequest) wire.HttpResponse {
	startedAt := time.Now()
	requestBody, bodyError := decodeRequestBody(request.Body)
	if bodyError != nil {
		return newErrorResponse(outboundTraceID, startedAt, 0, bodyError)
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

	httpResponse, err := http.DefaultClient.Do(httpRequest)
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

func mapErrorKind(err error) wire.ErrorKind {
	if errors.Is(err, context.Canceled) {
		return wire.ErrorKindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return wire.ErrorKindTimeout
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return wire.ErrorKindDNS
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return wire.ErrorKindConnectionRefused
		}
		if errors.Is(opErr.Err, syscall.ECONNRESET) {
			return wire.ErrorKindConnectionReset
		}
	}

	var certErr x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return wire.ErrorKindTLS
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return wire.ErrorKindConnectionRefused
	case strings.Contains(message, "certificate"), strings.Contains(message, "tls"):
		return wire.ErrorKindTLS
	case strings.Contains(message, "reset"):
		return wire.ErrorKindConnectionReset
	}

	return wire.ErrorKindUnknown
}
