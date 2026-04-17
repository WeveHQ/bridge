package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/healthz"
	"github.com/WeveHQ/bridge/internal/logging"
	"github.com/WeveHQ/bridge/internal/testsupport"
	"github.com/WeveHQ/bridge/internal/verifier"
	"github.com/WeveHQ/bridge/internal/wire"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	response, err := http.Get(testServer.URL + healthz.Path)
	if err != nil {
		t.Fatalf("healthz request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "OK" {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestDispatchRoundTrip(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type dispatchOutcome struct {
		response wire.HttpResponse
		status   int
	}

	postHeartbeat(t, testServer.URL, token)

	outcomeCh := make(chan dispatchOutcome, 1)
	go func() {
		response, status := dispatchRequest(t, ctx, testServer.URL, wire.DispatchRequest{
			OutboundTraceID: "ot_123",
			Req: wire.HttpRequest{
				Method:         "GET",
				URL:            "https://target.internal/test",
				DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
			},
		})
		outcomeCh <- dispatchOutcome{response: response, status: status}
	}()

	polled := pollRequest(t, testServer.URL, token)
	if polled.OutboundTraceID != "ot_123" {
		t.Fatalf("unexpected outbound trace id: %s", polled.OutboundTraceID)
	}

	postResponse(t, testServer.URL, token, wire.HttpResponse{
		OutboundTraceID: "ot_123",
		Status:          200,
		Headers:         []wire.HeaderEntry{{Name: "content-type", Value: "application/json"}},
		Body:            base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)),
	})

	select {
	case outcome := <-outcomeCh:
		if outcome.status != http.StatusOK {
			t.Fatalf("unexpected dispatch status: %d", outcome.status)
		}
		if outcome.response.Status != 200 {
			t.Fatalf("unexpected response status: %d", outcome.response.Status)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for dispatch")
	}
}

func TestDispatchFailsWhenBridgeOffline(t *testing.T) {
	t.Parallel()

	server, _ := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	requestBody := wire.MustJSON(wire.DispatchRequest{
		OutboundTraceID: "ot_123",
		Req: wire.HttpRequest{
			Method:         "GET",
			URL:            "https://target.internal/test",
			DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
		},
	})

	request, err := http.NewRequest(http.MethodPost, testServer.URL+wire.DispatchPathPrefix+"bridge_123", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set(bridgeHubSecretHeader, "internal-secret")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("dispatch request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", response.StatusCode)
	}
}

func TestDuplicateResponseIsIdempotent(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		postHeartbeat(t, testServer.URL, token)
		_, _ = dispatchRequest(t, ctx, testServer.URL, wire.DispatchRequest{
			OutboundTraceID: "ot_123",
			Req: wire.HttpRequest{
				Method:         "GET",
				URL:            "https://target.internal/test",
				DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
			},
		})
	}()

	_ = pollRequest(t, testServer.URL, token)

	response := wire.HttpResponse{
		OutboundTraceID: "ot_123",
		Status:          200,
		Body:            base64.StdEncoding.EncodeToString([]byte(`ok`)),
	}

	postResponse(t, testServer.URL, token, response)
	postResponse(t, testServer.URL, token, response)
}

func TestAuthenticateEdgeReturnsServiceUnavailableWhenVerifierFails(t *testing.T) {
	t.Parallel()

	server := NewServer(Config{
		TokenVerifier:  testsupport.StaticVerifier{Err: errors.New("boom")},
		HubSecret:      "internal-secret",
		PollHold:       100 * time.Millisecond,
		GlobalInFlight: 8,
	})
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	request, err := http.NewRequest(http.MethodPost, testServer.URL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
		BridgeVersion: "dev",
	})))
	if err != nil {
		t.Fatalf("create heartbeat request: %v", err)
	}
	request.Header.Set(authorizationHeader, "Bearer bridge-token")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("heartbeat request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", response.StatusCode)
	}
}

func TestBridgeStatusReportsHeartbeatAndWaiters(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	postHeartbeat(t, testServer.URL, token)

	pollErrCh := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, testServer.URL+wire.PollPath, bytes.NewReader(wire.MustJSON(wire.PollRequest{
			BridgeVersion: "dev",
		})))
		if err != nil {
			pollErrCh <- err
			return
		}
		request.Header.Set(authorizationHeader, "Bearer "+token)

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			pollErrCh <- err
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(response.Body)
			pollErrCh <- errors.New(string(body))
			return
		}
		pollErrCh <- nil
	}()

	deadline := time.After(2 * time.Second)
	for {
		status := getBridgeStatus(t, testServer.URL, "bridge_123")
		if status.WaiterCount == 1 {
			if !status.Alive {
				t.Fatal("expected bridge to be alive")
			}
			if status.BridgeVersion != "dev" {
				t.Fatalf("unexpected bridge version: %s", status.BridgeVersion)
			}
			if status.Hostname != "test-host" {
				t.Fatalf("unexpected hostname: %s", status.Hostname)
			}
			if status.OS != "darwin" || status.Arch != "arm64" {
				t.Fatalf("unexpected platform: %s/%s", status.OS, status.Arch)
			}
			if status.LastHeartbeatAtUnixMs == nil {
				t.Fatal("expected last heartbeat timestamp")
			}
			break
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for waiter count")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if err := <-pollErrCh; err != nil {
		t.Fatalf("poll request failed: %v", err)
	}
}

func TestBridgeStatusReportsInFlightDispatches(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	postHeartbeat(t, testServer.URL, token)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	outcomeCh := make(chan error, 1)
	go func() {
		_, status := dispatchRequest(t, ctx, testServer.URL, wire.DispatchRequest{
			OutboundTraceID: "ot_123",
			Req: wire.HttpRequest{
				Method:         "GET",
				URL:            "https://target.internal/test",
				DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
			},
		})
		if status != http.StatusOK {
			outcomeCh <- errors.New("unexpected dispatch status")
			return
		}
		outcomeCh <- nil
	}()

	_ = pollRequest(t, testServer.URL, token)

	deadline := time.After(2 * time.Second)
	for {
		status := getBridgeStatus(t, testServer.URL, "bridge_123")
		if status.InFlightDispatchCount == 1 {
			if !status.Alive {
				t.Fatal("expected bridge to be alive")
			}
			break
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for in-flight dispatch count")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	postResponse(t, testServer.URL, token, wire.HttpResponse{
		OutboundTraceID: "ot_123",
		Status:          200,
		Body:            base64.StdEncoding.EncodeToString([]byte(`ok`)),
	})

	if err := <-outcomeCh; err != nil {
		t.Fatalf("dispatch outcome failed: %v", err)
	}
}

func TestHubLogsBridgeLifecycleTransitionsOnce(t *testing.T) {
	var buffer bytes.Buffer
	logger, err := logging.New(&buffer, logging.Config{
		Level:  logging.LevelDebug,
		Format: logging.FormatText,
	})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	server, token := newTestServerWithOptions(func() time.Time { return now }, logger)
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	postHeartbeat(t, testServer.URL, token)
	if count := strings.Count(buffer.String(), "bridge connected"); count != 1 {
		t.Fatalf("expected one bridge connected log, got %d: %s", count, buffer.String())
	}

	now = now.Add(heartbeatTTL + time.Second)
	status := getBridgeStatus(t, testServer.URL, "bridge_123")
	if status.Alive {
		t.Fatal("expected bridge to be offline")
	}
	if count := strings.Count(buffer.String(), "bridge went offline"); count != 1 {
		t.Fatalf("expected one bridge offline log, got %d: %s", count, buffer.String())
	}

	_ = getBridgeStatus(t, testServer.URL, "bridge_123")
	if count := strings.Count(buffer.String(), "bridge went offline"); count != 1 {
		t.Fatalf("expected offline log to be deduplicated, got %d: %s", count, buffer.String())
	}

	postHeartbeat(t, testServer.URL, token)
	if count := strings.Count(buffer.String(), "bridge reconnected"); count != 1 {
		t.Fatalf("expected one bridge reconnected log, got %d: %s", count, buffer.String())
	}
}

func newTestServer() (*Server, string) {
	return newTestServerWithOptions(
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		nil,
	)
}

func newTestServerWithOptions(now func() time.Time, logger *slog.Logger) (*Server, string) {
	token := "bridge-token"
	server := NewServer(Config{
		TokenVerifier: testsupport.StaticVerifier{
			ClaimsByToken: map[string]verifier.Claims{
				token: {
					TenantID: "tenant_123",
					BridgeID: "bridge_123",
				},
			},
		},
		HubSecret:      "internal-secret",
		PollHold:       100 * time.Millisecond,
		GlobalInFlight: 8,
		Now:            now,
		Logger:         logger,
	})

	return server, token
}

func dispatchRequest(t *testing.T, ctx context.Context, baseURL string, dispatch wire.DispatchRequest) (wire.HttpResponse, int) {
	t.Helper()
	return testsupport.DispatchRequestTo(t, ctx, baseURL, "bridge_123", "internal-secret", dispatch)
}

func pollRequest(t *testing.T, baseURL string, token string) wire.PollResponse {
	t.Helper()
	return testsupport.PollRequest(t, baseURL, token)
}

func postResponse(t *testing.T, baseURL string, token string, payload wire.HttpResponse) {
	t.Helper()
	testsupport.PostResponse(t, baseURL, token, payload)
}

func postHeartbeat(t *testing.T, baseURL string, token string) {
	t.Helper()
	testsupport.PostHeartbeat(t, baseURL, token, wire.HeartbeatRequest{
		BridgeVersion: "dev",
		OS:            "darwin",
		Arch:          "arm64",
		UptimeSec:     42,
		InFlight:      3,
		Hostname:      "test-host",
	})
}

func getBridgeStatus(t *testing.T, baseURL string, bridgeID string) wire.BridgeStatusResponse {
	t.Helper()
	return testsupport.GetBridgeStatus(t, baseURL, bridgeID, "internal-secret")
}

func newTwoBridgeServer() (*Server, map[string]string) {
	tokens := map[string]string{
		"bridge_a": "token-a",
		"bridge_b": "token-b",
	}
	claims := map[string]verifier.Claims{}
	for bridgeID, token := range tokens {
		claims[token] = verifier.Claims{TenantID: "tenant_123", BridgeID: bridgeID}
	}
	server := NewServer(Config{
		TokenVerifier:  testsupport.StaticVerifier{ClaimsByToken: claims},
		HubSecret:      "internal-secret",
		PollHold:       100 * time.Millisecond,
		GlobalInFlight: 8,
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	return server, tokens
}

func postHeartbeatFor(t *testing.T, baseURL string, token string) {
	t.Helper()
	postHeartbeat(t, baseURL, token)
}

func dispatchRequestTo(t *testing.T, ctx context.Context, baseURL string, bridgeID string, dispatch wire.DispatchRequest) (wire.HttpResponse, int) {
	t.Helper()
	return testsupport.DispatchRequestTo(t, ctx, baseURL, bridgeID, "internal-secret", dispatch)
}

func postRawResponse(t *testing.T, baseURL string, token string, payload wire.HttpResponse) *http.Response {
	t.Helper()
	return testsupport.PostRawResponse(t, baseURL, token, payload)
}

func TestDispatchIsolatedByBridgeOnDuplicateTraceID(t *testing.T) {
	t.Parallel()

	server, tokens := newTwoBridgeServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	postHeartbeatFor(t, testServer.URL, tokens["bridge_a"])
	postHeartbeatFor(t, testServer.URL, tokens["bridge_b"])

	type outcome struct {
		response wire.HttpResponse
		status   int
	}
	resultA := make(chan outcome, 1)
	resultB := make(chan outcome, 1)

	dispatchPayload := func(bridgeID string) wire.DispatchRequest {
		return wire.DispatchRequest{
			OutboundTraceID: "ot_dup",
			Req: wire.HttpRequest{
				Method:         "GET",
				URL:            "https://target.internal/" + bridgeID,
				DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
			},
		}
	}

	go func() {
		resp, status := dispatchRequestTo(t, ctx, testServer.URL, "bridge_a", dispatchPayload("bridge_a"))
		resultA <- outcome{response: resp, status: status}
	}()
	go func() {
		resp, status := dispatchRequestTo(t, ctx, testServer.URL, "bridge_b", dispatchPayload("bridge_b"))
		resultB <- outcome{response: resp, status: status}
	}()

	polledA := pollRequest(t, testServer.URL, tokens["bridge_a"])
	polledB := pollRequest(t, testServer.URL, tokens["bridge_b"])
	if polledA.OutboundTraceID != "ot_dup" || polledB.OutboundTraceID != "ot_dup" {
		t.Fatalf("unexpected polled trace ids: %q %q", polledA.OutboundTraceID, polledB.OutboundTraceID)
	}
	if polledA.Req.URL == polledB.Req.URL {
		t.Fatalf("each bridge should receive its own dispatch, got same URL: %s", polledA.Req.URL)
	}

	bodyA := base64.StdEncoding.EncodeToString([]byte(`{"from":"a"}`))
	bodyB := base64.StdEncoding.EncodeToString([]byte(`{"from":"b"}`))
	postResponse(t, testServer.URL, tokens["bridge_a"], wire.HttpResponse{
		OutboundTraceID: "ot_dup",
		Status:          200,
		Body:            bodyA,
	})
	postResponse(t, testServer.URL, tokens["bridge_b"], wire.HttpResponse{
		OutboundTraceID: "ot_dup",
		Status:          200,
		Body:            bodyB,
	})

	var outA, outB outcome
	select {
	case outA = <-resultA:
	case <-ctx.Done():
		t.Fatal("timed out waiting for bridge_a dispatch")
	}
	select {
	case outB = <-resultB:
	case <-ctx.Done():
		t.Fatal("timed out waiting for bridge_b dispatch")
	}

	if outA.status != http.StatusOK || outB.status != http.StatusOK {
		t.Fatalf("unexpected statuses: a=%d b=%d", outA.status, outB.status)
	}
	if outA.response.Body != bodyA {
		t.Fatalf("bridge_a got wrong body: %q", outA.response.Body)
	}
	if outB.response.Body != bodyB {
		t.Fatalf("bridge_b got wrong body: %q", outB.response.Body)
	}
}

func TestResponseFromWrongBridgeIsRejected(t *testing.T) {
	t.Parallel()

	server, tokens := newTwoBridgeServer()
	testServer := httptest.NewServer(server.Handler())
	defer func() { testServer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	postHeartbeatFor(t, testServer.URL, tokens["bridge_a"])
	postHeartbeatFor(t, testServer.URL, tokens["bridge_b"])

	type outcome struct {
		response wire.HttpResponse
		status   int
	}
	resultA := make(chan outcome, 1)

	go func() {
		resp, status := dispatchRequestTo(t, ctx, testServer.URL, "bridge_a", wire.DispatchRequest{
			OutboundTraceID: "ot_xyz",
			Req: wire.HttpRequest{
				Method:         "GET",
				URL:            "https://target.internal/test",
				DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
			},
		})
		resultA <- outcome{response: resp, status: status}
	}()

	polled := pollRequest(t, testServer.URL, tokens["bridge_a"])
	if polled.OutboundTraceID != "ot_xyz" {
		t.Fatalf("unexpected trace id: %s", polled.OutboundTraceID)
	}

	maliciousBody := base64.StdEncoding.EncodeToString([]byte(`{"from":"b"}`))
	bResponse := postRawResponse(t, testServer.URL, tokens["bridge_b"], wire.HttpResponse{
		OutboundTraceID: "ot_xyz",
		Status:          200,
		Body:            maliciousBody,
	})
	defer func() { _ = bResponse.Body.Close() }()

	if bResponse.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(bResponse.Body)
		t.Fatalf("expected 404 for wrong-bridge response, got %d: %s", bResponse.StatusCode, body)
	}

	select {
	case <-resultA:
		t.Fatal("bridge_a dispatch completed before its own response was posted")
	case <-time.After(50 * time.Millisecond):
	}

	aBody := base64.StdEncoding.EncodeToString([]byte(`{"from":"a"}`))
	postResponse(t, testServer.URL, tokens["bridge_a"], wire.HttpResponse{
		OutboundTraceID: "ot_xyz",
		Status:          200,
		Body:            aBody,
	})

	select {
	case outA := <-resultA:
		if outA.status != http.StatusOK {
			t.Fatalf("unexpected dispatch status: %d", outA.status)
		}
		if outA.response.Body != aBody {
			t.Fatalf("bridge_a received wrong body: %q", outA.response.Body)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for bridge_a dispatch")
	}
}
