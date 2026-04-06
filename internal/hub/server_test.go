package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/WeveHQ/weve-bridge/internal/verifier"
	"github.com/WeveHQ/weve-bridge/internal/wire"
)

func TestDispatchRoundTrip(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

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
	defer testServer.Close()

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
	request.Header.Set(internalSecretHeader, "internal-secret")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("dispatch request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", response.StatusCode)
	}
}

func TestDuplicateResponseIsIdempotent(t *testing.T) {
	t.Parallel()

	server, token := newTestServer()
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

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
		TokenVerifier:  staticVerifier{err: errors.New("boom")},
		InternalSecret: "internal-secret",
		PollHold:       100 * time.Millisecond,
		GlobalInFlight: 8,
	})
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

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
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", response.StatusCode)
	}
}

func newTestServer() (*Server, string) {
	token := "bridge-token"
	server := NewServer(Config{
		TokenVerifier: staticVerifier{
			claimsByToken: map[string]verifier.Claims{
				token: {
					TenantID: "tenant_123",
					BridgeID: "bridge_123",
				},
			},
		},
		InternalSecret: "internal-secret",
		PollHold:       100 * time.Millisecond,
		GlobalInFlight: 8,
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})

	return server, token
}

func dispatchRequest(t *testing.T, ctx context.Context, baseURL string, dispatch wire.DispatchRequest) (wire.HttpResponse, int) {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+wire.DispatchPathPrefix+"bridge_123", bytes.NewReader(wire.MustJSON(dispatch)))
	if err != nil {
		t.Fatalf("create dispatch request: %v", err)
	}
	request.Header.Set(internalSecretHeader, "internal-secret")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("dispatch request failed: %v", err)
	}
	defer response.Body.Close()

	var parsed wire.HttpResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode dispatch response: %v", err)
	}

	return parsed, response.StatusCode
}

func pollRequest(t *testing.T, baseURL string, token string) wire.PollResponse {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.PollPath, bytes.NewReader(wire.MustJSON(wire.PollRequest{BridgeVersion: "dev"})))
	if err != nil {
		t.Fatalf("create poll request: %v", err)
	}
	request.Header.Set(authorizationHeader, "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("poll request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected poll status %d: %s", response.StatusCode, string(body))
	}

	var parsed wire.PollResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}

	return parsed
}

func postResponse(t *testing.T, baseURL string, token string, payload wire.HttpResponse) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.ResponsePathPrefix+payload.OutboundTraceID, bytes.NewReader(wire.MustJSON(payload)))
	if err != nil {
		t.Fatalf("create response request: %v", err)
	}
	request.Header.Set(authorizationHeader, "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post response failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected response post status %d: %s", response.StatusCode, string(body))
	}
}

func postHeartbeat(t *testing.T, baseURL string, token string) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
		BridgeVersion: "dev",
		OS:            "darwin",
		Arch:          "arm64",
		Hostname:      "test-host",
	})))
	if err != nil {
		t.Fatalf("create heartbeat request: %v", err)
	}
	request.Header.Set(authorizationHeader, "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("heartbeat request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected heartbeat status %d: %s", response.StatusCode, string(body))
	}
}

type staticVerifier struct {
	claimsByToken map[string]verifier.Claims
	err           error
}

func (stub staticVerifier) Verify(_ context.Context, token string) (verifier.Claims, error) {
	if stub.err != nil {
		return verifier.Claims{}, stub.err
	}

	claims, ok := stub.claimsByToken[token]
	if !ok {
		return verifier.Claims{}, verifier.ErrInvalidToken
	}

	return claims, nil
}
