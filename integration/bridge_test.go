package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/WeveHQ/weve-bridge/internal/wire"
)

func TestBridgeBinaryDispatchesRequests(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if string(body) != `{"from":"integration"}` {
			t.Fatalf("unexpected request body: %s", string(body))
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"via":"binary"}`))
	}))
	defer target.Close()

	binaryPath := buildBinary(t)
	token := "bridge-token"
	hubAddr := freeAddr(t)
	verifyURL := startVerifier(t, token)

	hubCmd := startProcess(t, binaryPath, []string{"hub", "--listen=" + hubAddr}, []string{
		"WEVE_BRIDGE_VERIFY_TOKEN_URL=" + verifyURL,
		"WEVE_BRIDGE_INTERNAL_SECRET=internal-secret",
		"WEVE_BRIDGE_POLL_HOLD_SECONDS=1",
		"WEVE_BRIDGE_GLOBAL_IN_FLIGHT=8",
	})
	defer stopProcess(hubCmd)

	waitForHub(t, "http://"+hubAddr)

	edgeCmd := startProcess(t, binaryPath, []string{"edge", "--token=" + token, "--hub-url=http://" + hubAddr}, []string{
		"WEVE_BRIDGE_POLL_CONCURRENCY=2",
		"WEVE_BRIDGE_HEARTBEAT_SECONDS=1",
		"WEVE_BRIDGE_POLL_TIMEOUT_MS=1500",
	})
	defer stopProcess(edgeCmd)

	response := dispatchWithRetry(t, "http://"+hubAddr, wire.DispatchRequest{
		OutboundTraceID: "ot_integration",
		Req: wire.HttpRequest{
			Method:         "POST",
			URL:            target.URL + "/search?q=integration",
			DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
			Headers: []wire.HeaderEntry{
				{Name: "Content-Type", Value: "application/json"},
			},
			Body: base64.StdEncoding.EncodeToString([]byte(`{"from":"integration"}`)),
		},
	})

	if response.Status != http.StatusCreated {
		t.Fatalf("unexpected response status: %d", response.Status)
	}

	body, err := base64.StdEncoding.DecodeString(response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(body) != `{"via":"binary"}` {
		t.Fatalf("unexpected response body: %s", string(body))
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()

	outputPath := filepath.Join(t.TempDir(), "weve-bridge")
	command := exec.Command("go", "build", "-o", outputPath, "./cmd/bridge")
	command.Dir = projectRoot(t)
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build bridge binary: %v\n%s", err, string(output))
	}

	return outputPath
}

func startVerifier(t *testing.T, token string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "invalid token", http.StatusUnauthorized)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"tenantId":"tenant_123","bridgeId":"bridge_123"}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().String()
}

func startProcess(t *testing.T, binaryPath string, args []string, env []string) *exec.Cmd {
	t.Helper()

	command := exec.Command(binaryPath, args...)
	command.Dir = projectRoot(t)
	command.Env = append(os.Environ(), env...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("start process %v: %v", args, err)
	}

	return command
}

func stopProcess(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}

	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
}

func waitForHub(t *testing.T, baseURL string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodPost, baseURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
			BridgeVersion: "probe",
		})))
		if err != nil {
			t.Fatalf("create readiness request: %v", err)
		}

		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("timed out waiting for hub")
}

func dispatchWithRetry(t *testing.T, baseURL string, payload wire.DispatchRequest) wire.HttpResponse {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		response, reject, err := dispatchOnce(baseURL, payload)
		if err == nil {
			return response
		}
		if reject == nil || reject.Error.Code != wire.BridgeOfflineCode {
			t.Fatalf("dispatch request failed: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("timed out waiting for bridge dispatch")
	return wire.HttpResponse{}
}

func dispatchOnce(baseURL string, payload wire.DispatchRequest) (wire.HttpResponse, *wire.DispatchReject, error) {
	request, err := http.NewRequest(http.MethodPost, baseURL+wire.DispatchPathPrefix+"bridge_123", bytes.NewReader(wire.MustJSON(payload)))
	if err != nil {
		return wire.HttpResponse{}, nil, err
	}
	request.Header.Set("X-Internal-Secret", "internal-secret")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return wire.HttpResponse{}, nil, err
	}
	defer response.Body.Close()

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

func projectRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	return root
}
