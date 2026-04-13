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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/logging"
	"github.com/WeveHQ/bridge/internal/wire"
)

const (
	retryInterval          = 10 * time.Second
	pollRateLimitedBackoff = 1 * time.Second
)

var errPollRateLimited = errors.New("poll rate limited by hub")

type Config struct {
	Token             string
	HubURL            string
	PollConcurrency   int
	HeartbeatInterval time.Duration
	PollTimeout       time.Duration
	AllowedHosts      []string
	Client            *http.Client
	Logger            *slog.Logger
}

type Runner struct {
	client            *http.Client
	token             string
	hubURL            string
	pollConcurrency   int
	heartbeatInterval time.Duration
	pollTimeout       time.Duration
	allowedHosts      []string
	startedAt         time.Time
	inFlight          atomic.Int32
	logger            *slog.Logger

	stateMu          sync.Mutex
	connectedLogged  bool
	heartbeatFailing bool
	pollFailing      bool
}

func NewRunner(cfg Config) *Runner {
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = logging.Discard()
	}

	return &Runner{
		client:            client,
		token:             cfg.Token,
		hubURL:            strings.TrimRight(cfg.HubURL, "/"),
		pollConcurrency:   cfg.PollConcurrency,
		heartbeatInterval: cfg.HeartbeatInterval,
		pollTimeout:       cfg.PollTimeout,
		allowedHosts:      cfg.AllowedHosts,
		startedAt:         time.Now(),
		logger:            logger,
	}
}

func (runner *Runner) Run(ctx context.Context) error {
	runner.logger.Info("edge starting",
		"hubURL", runner.hubURL,
		"pollConcurrency", runner.pollConcurrency,
		"heartbeatInterval", runner.heartbeatInterval.String(),
		"pollTimeout", runner.pollTimeout.String(),
		"allowedHostsCount", len(runner.allowedHosts),
		"allowListEnabled", len(runner.allowedHosts) > 0,
	)
	defer runner.logger.Info("edge stopped")

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, runner.pollConcurrency+1)
	go runner.runHeartbeatLoop(heartbeatCtx, errCh)

	for index := 0; index < runner.pollConcurrency; index++ {
		go runner.runPollSlot(ctx, errCh)
	}

	select {
	case <-ctx.Done():
		runner.logger.Info("edge shutting down", "reason", ctx.Err())
		return nil
	case err := <-errCh:
		if err == nil {
			return nil
		}
		runner.logger.Error("edge runner failed", "error", err)
		return err
	}
}

func (runner *Runner) runHeartbeatLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(runner.heartbeatInterval)
	defer ticker.Stop()

	for {
		if err := runner.sendHeartbeat(ctx); err != nil && !errors.Is(err, context.Canceled) {
			runner.markHeartbeatFailure(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}
		runner.markHeartbeatSuccess()
		runner.logger.Debug("heartbeat acknowledged")

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

			if errors.Is(err, errPollRateLimited) {
				runner.markPollFailure(err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(pollRateLimitedBackoff):
					continue
				}
			}

			runner.markPollFailure(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}
		runner.markPollSuccess()
		if !ok {
			runner.logger.Debug("poll returned no work")
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}

		go runner.runPollSlot(ctx, errCh)
		runner.inFlight.Add(1)
		if err := runner.handleDispatch(ctx, dispatch); err != nil {
			runner.logger.Warn("dispatch response post failed",
				"outboundTraceId", dispatch.OutboundTraceID,
				"error", err,
			)
		}
		runner.inFlight.Add(-1)
		return
	}
}

func (runner *Runner) handleDispatch(ctx context.Context, dispatch wire.PollResponse) error {
	runner.logger.Debug("dispatch received",
		"outboundTraceId", dispatch.OutboundTraceID,
		"method", dispatch.Req.Method,
		"url", dispatch.Req.URL,
	)

	response := ExecuteRequest(dispatch.OutboundTraceID, dispatch.Req, runner.allowedHosts)
	if err := runner.postResponse(ctx, response); err != nil {
		return err
	}

	attrs := runner.dispatchLogAttrs(dispatch, response)
	if response.Meta.Error != nil {
		runner.logger.Warn("dispatch completed with execution error", attrs...)
		return nil
	}

	runner.logger.Info("dispatch completed", attrs...)
	return nil
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
	if response.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, response.Body)
		return wire.PollResponse{}, false, errPollRateLimited
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

func (runner *Runner) markHeartbeatFailure(err error) {
	runner.stateMu.Lock()
	shouldLog := !runner.heartbeatFailing
	runner.heartbeatFailing = true
	runner.stateMu.Unlock()

	if shouldLog {
		runner.logger.Warn("heartbeat failed",
			"error", err,
			"retryIn", retryInterval.String(),
		)
	}
}

func (runner *Runner) markHeartbeatSuccess() {
	runner.stateMu.Lock()
	firstSuccess := !runner.connectedLogged
	recovered := runner.heartbeatFailing
	runner.connectedLogged = true
	runner.heartbeatFailing = false
	runner.stateMu.Unlock()

	if firstSuccess {
		runner.logger.Info("connected to hub", "hubURL", runner.hubURL)
		return
	}
	if recovered {
		runner.logger.Info("heartbeat recovered", "hubURL", runner.hubURL)
	}
}

func (runner *Runner) markPollFailure(err error) {
	runner.stateMu.Lock()
	shouldLog := !runner.pollFailing
	runner.pollFailing = true
	runner.stateMu.Unlock()

	message := "poll failed"
	retryIn := retryInterval
	if errors.Is(err, errPollRateLimited) {
		message = "poll rate limited by hub"
		retryIn = pollRateLimitedBackoff
	}

	if shouldLog {
		runner.logger.Warn(message,
			"error", err,
			"retryIn", retryIn.String(),
		)
	}
}

func (runner *Runner) markPollSuccess() {
	runner.stateMu.Lock()
	recovered := runner.pollFailing
	runner.pollFailing = false
	runner.stateMu.Unlock()

	if recovered {
		runner.logger.Info("polling recovered", "hubURL", runner.hubURL)
	}
}

func (runner *Runner) dispatchLogAttrs(dispatch wire.PollResponse, response wire.HttpResponse) []any {
	attrs := []any{
		"outboundTraceId", dispatch.OutboundTraceID,
		"method", dispatch.Req.Method,
		"url", dispatch.Req.URL,
		"durationMs", response.Meta.DurationMs,
		"bytesOut", response.Meta.BytesOut,
		"bytesIn", response.Meta.BytesIn,
	}

	if parsedURL, err := url.Parse(dispatch.Req.URL); err == nil {
		attrs = append(attrs, "targetHost", parsedURL.Hostname())
	}

	if response.Status != 0 {
		attrs = append(attrs, "status", response.Status)
	}

	if response.Meta.Error != nil {
		attrs = append(attrs,
			"errorKind", response.Meta.Error.Kind,
			"errorMessage", response.Meta.Error.Message,
			"outcome", "execution_error",
		)
		return attrs
	}

	attrs = append(attrs, "outcome", "response_posted")
	return attrs
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

func ExecuteRequest(outboundTraceID string, request wire.HttpRequest, allowedHosts []string) wire.HttpResponse {
	startedAt := time.Now()
	requestBody, bodyError := decodeRequestBody(request.Body)
	if bodyError != nil {
		return newErrorResponse(outboundTraceID, startedAt, 0, bodyError)
	}

	if len(allowedHosts) > 0 {
		host := ""
		if parsed, err := url.Parse(request.URL); err == nil {
			host = strings.ToLower(parsed.Hostname())
		}
		if !hostAllowed(host, allowedHosts) {
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
