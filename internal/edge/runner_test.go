package edge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/WeveHQ/weve-bridge/internal/auth"
	"github.com/WeveHQ/weve-bridge/internal/hub"
	"github.com/WeveHQ/weve-bridge/internal/wire"
	"github.com/golang-jwt/jwt/v5"
)

func TestExecuteRequestSuccess(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Test") != "value" {
			t.Fatalf("unexpected header: %s", request.Header.Get("X-Test"))
		}
		writer.Header().Add("Set-Cookie", "a=1")
		writer.Header().Add("Set-Cookie", "b=2")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	response := ExecuteRequest("ot_123", wire.HttpRequest{
		Method:         "GET",
		URL:            target.URL,
		DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
		Headers:        []wire.HeaderEntry{{Name: "X-Test", Value: "value"}},
	})

	if response.Status != http.StatusAccepted {
		t.Fatalf("unexpected status: %d", response.Status)
	}
	if response.Meta.Error != nil {
		t.Fatalf("unexpected execution error: %#v", response.Meta.Error)
	}
	body, err := base64.StdEncoding.DecodeString(response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected response body: %s", string(body))
	}
}

func TestExecuteRequestMapsConnectionRefused(t *testing.T) {
	t.Parallel()

	response := ExecuteRequest("ot_123", wire.HttpRequest{
		Method:         "GET",
		URL:            "http://127.0.0.1:1",
		DeadlineUnixMs: uint64(time.Now().Add(2 * time.Second).UnixMilli()),
	})

	if response.Meta.Error == nil {
		t.Fatal("expected execution error")
	}
	if response.Meta.Error.Kind != wire.ErrorKindConnectionRefused {
		t.Fatalf("unexpected error kind: %s", response.Meta.Error.Kind)
	}
}

func TestRunnerBridgesHubDispatchToTarget(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "q=bridge" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"source":"edge"}`))
	}))
	defer target.Close()

	now := time.Unix(1_700_000_000, 0).UTC()
	token, err := auth.SignBridgeToken([]byte("token-secret"), auth.BridgeClaims{
		TenantID:         "tenant_123",
		BridgeID:         "bridge_123",
		RegisteredClaims: jwtRegisteredClaims(now),
	})
	if err != nil {
		t.Fatalf("sign bridge token: %v", err)
	}

	hubServer := hub.NewServer(hub.Config{
		TokenSecret:    []byte("token-secret"),
		InternalSecret: "internal-secret",
		PollHold:       200 * time.Millisecond,
		GlobalInFlight: 8,
		Now:            func() time.Time { return now },
	})

	hubHTTP := httptest.NewServer(hubServer.Handler())
	defer hubHTTP.Close()

	runner := NewRunner(Config{
		Token:             token,
		HubURL:            hubHTTP.URL,
		PollConcurrency:   2,
		HeartbeatInterval: 50 * time.Millisecond,
		PollTimeout:       500 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = runner.Run(ctx)
	}()

	waitForHeartbeat(t, hubHTTP.URL, token)

	request, err := http.NewRequest(http.MethodPost, hubHTTP.URL+wire.DispatchPathPrefix+"bridge_123", bytes.NewReader(wire.MustJSON(wire.DispatchRequest{
		OutboundTraceID: "ot_123",
		Req: wire.HttpRequest{
			Method:         "GET",
			URL:            target.URL + "?q=bridge",
			DeadlineUnixMs: uint64(time.Now().Add(5 * time.Second).UnixMilli()),
		},
	})))
	if err != nil {
		t.Fatalf("create dispatch request: %v", err)
	}
	request.Header.Set("X-Internal-Secret", "internal-secret")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("dispatch via hub: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected dispatch status %d: %s", response.StatusCode, string(body))
	}

	var parsed wire.HttpResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode dispatch response: %v", err)
	}

	body, err := base64.StdEncoding.DecodeString(parsed.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(body) != `{"source":"edge"}` {
		t.Fatalf("unexpected dispatch body: %s", string(body))
	}
}

func waitForHeartbeat(t *testing.T, baseURL string, token string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodPost, baseURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
			BridgeVersion: "probe",
		})))
		if err != nil {
			t.Fatalf("create heartbeat probe: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)

		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("timed out waiting for heartbeat")
}

func jwtRegisteredClaims(now time.Time) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}
