package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

func WaitForHub(t testing.TB, baseURL string, timeout time.Duration, interval time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodPost, baseURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
			BridgeVersion: "probe",
		})))
		if err != nil {
			t.Fatalf("create readiness request: %v", err)
		}

		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return
			}
		}

		time.Sleep(interval)
	}

	t.Fatal("timed out waiting for hub")
}

func WaitForHeartbeat(t testing.TB, baseURL string, token string, timeout time.Duration, interval time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
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
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(interval)
	}

	t.Fatal("timed out waiting for heartbeat")
}

func DispatchRequestTo(t testing.TB, ctx context.Context, baseURL string, bridgeID string, hubSecret string, dispatch wire.DispatchRequest) (wire.HttpResponse, int) {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+wire.DispatchPathPrefix+bridgeID, bytes.NewReader(wire.MustJSON(dispatch)))
	if err != nil {
		t.Fatalf("create dispatch request: %v", err)
	}
	request.Header.Set("X-Bridge-Hub-Secret", hubSecret)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("dispatch request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	var parsed wire.HttpResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode dispatch response: %v", err)
	}

	return parsed, response.StatusCode
}

func DispatchOnce(baseURL string, bridgeID string, hubSecret string, payload wire.DispatchRequest) (wire.HttpResponse, *wire.DispatchReject, error) {
	request, err := http.NewRequest(http.MethodPost, baseURL+wire.DispatchPathPrefix+bridgeID, bytes.NewReader(wire.MustJSON(payload)))
	if err != nil {
		return wire.HttpResponse{}, nil, err
	}
	request.Header.Set("X-Bridge-Hub-Secret", hubSecret)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return wire.HttpResponse{}, nil, err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return wire.HttpResponse{}, nil, err
	}

	if response.StatusCode == http.StatusOK {
		var parsed wire.HttpResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return wire.HttpResponse{}, nil, err
		}
		return parsed, nil, nil
	}

	var reject wire.DispatchReject
	if err := json.Unmarshal(body, &reject); err != nil {
		return wire.HttpResponse{}, nil, err
	}

	return wire.HttpResponse{}, &reject, errors.New(reject.Error.Code)
}

func DispatchWithRetry(t testing.TB, baseURL string, bridgeID string, hubSecret string, payload wire.DispatchRequest, timeout time.Duration, interval time.Duration) wire.HttpResponse {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, reject, err := DispatchOnce(baseURL, bridgeID, hubSecret, payload)
		if err == nil {
			return response
		}
		if reject == nil || reject.Error.Code != wire.BridgeOfflineCode {
			t.Fatalf("dispatch request failed: %v", err)
		}

		time.Sleep(interval)
	}

	t.Fatal("timed out waiting for bridge dispatch")
	return wire.HttpResponse{}
}

func PollRequest(t testing.TB, baseURL string, token string) wire.PollResponse {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.PollPath, bytes.NewReader(wire.MustJSON(wire.PollRequest{BridgeVersion: "dev"})))
	if err != nil {
		t.Fatalf("create poll request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("poll request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

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

func PostRawResponse(t testing.TB, baseURL string, token string, payload wire.HttpResponse) *http.Response {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.ResponsePathPrefix+payload.OutboundTraceID, bytes.NewReader(wire.MustJSON(payload)))
	if err != nil {
		t.Fatalf("create response request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post response failed: %v", err)
	}
	return response
}

func PostResponse(t testing.TB, baseURL string, token string, payload wire.HttpResponse) {
	t.Helper()

	response := PostRawResponse(t, baseURL, token, payload)
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected response post status %d: %s", response.StatusCode, string(body))
	}
}

func PostHeartbeat(t testing.TB, baseURL string, token string, heartbeat wire.HeartbeatRequest) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, baseURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(heartbeat)))
	if err != nil {
		t.Fatalf("create heartbeat request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("heartbeat request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected heartbeat status %d: %s", response.StatusCode, string(body))
	}
}

func GetBridgeStatus(t testing.TB, baseURL string, bridgeID string, hubSecret string) wire.BridgeStatusResponse {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, baseURL+wire.BridgeStatusPathPrefix+bridgeID+wire.BridgeStatusPathSuffix, nil)
	if err != nil {
		t.Fatalf("create bridge status request: %v", err)
	}
	request.Header.Set("X-Bridge-Hub-Secret", hubSecret)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("bridge status request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected bridge status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed wire.BridgeStatusResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode bridge status response: %v", err)
	}

	return parsed
}
