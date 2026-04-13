package integration

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/testsupport"
	"github.com/WeveHQ/bridge/internal/wire"
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
	defer func() { target.Close() }()

	binaryPath := buildBinary(t)
	token := "bridge-token"
	hubAddr := freeAddr(t)
	verifyURL := testsupport.StartVerifierServer(t, token, "verifier-secret")

	hubCmd := startProcess(t, binaryPath, []string{"hub", "--listen=" + hubAddr}, []string{
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL=" + verifyURL,
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET=verifier-secret",
		"WEVE_BRIDGE_HUB_SECRET=internal-secret",
		"WEVE_BRIDGE_HUB_POLL_HOLD_SECONDS=1",
		"WEVE_BRIDGE_HUB_GLOBAL_IN_FLIGHT=8",
	})
	defer stopProcess(hubCmd)

	testsupport.WaitForHub(t, "http://"+hubAddr, 5*time.Second, 50*time.Millisecond)

	edgeCmd := startProcess(t, binaryPath, []string{"edge", "--token=" + token, "--hub-url=http://" + hubAddr}, []string{
		"WEVE_BRIDGE_EDGE_POLL_CONCURRENCY=2",
		"WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS=1",
		"WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS=1500",
	})
	defer stopProcess(edgeCmd)

	response := testsupport.DispatchWithRetry(t, "http://"+hubAddr, "bridge_123", "internal-secret", wire.DispatchRequest{
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
	}, 8*time.Second, 100*time.Millisecond)

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

func TestBridgeBinaryRejectsDisallowedHost(t *testing.T) {
	t.Parallel()

	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached = true
		writer.WriteHeader(http.StatusOK)
	}))
	defer func() { target.Close() }()

	binaryPath := buildBinary(t)
	token := "bridge-token"
	hubAddr := freeAddr(t)
	verifyURL := testsupport.StartVerifierServer(t, token, "verifier-secret")

	hubCmd := startProcess(t, binaryPath, []string{"hub", "--listen=" + hubAddr}, []string{
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL=" + verifyURL,
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET=verifier-secret",
		"WEVE_BRIDGE_HUB_SECRET=internal-secret",
		"WEVE_BRIDGE_HUB_POLL_HOLD_SECONDS=1",
		"WEVE_BRIDGE_HUB_GLOBAL_IN_FLIGHT=8",
	})
	defer stopProcess(hubCmd)

	testsupport.WaitForHub(t, "http://"+hubAddr, 5*time.Second, 50*time.Millisecond)

	edgeCmd := startProcess(t, binaryPath, []string{"edge", "--token=" + token, "--hub-url=http://" + hubAddr}, []string{
		"WEVE_BRIDGE_EDGE_POLL_CONCURRENCY=2",
		"WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS=1",
		"WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS=1500",
		"WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=allowed.example,another.example",
	})
	defer stopProcess(edgeCmd)

	response := testsupport.DispatchWithRetry(t, "http://"+hubAddr, "bridge_123", "internal-secret", wire.DispatchRequest{
		OutboundTraceID: "ot_host_blocked",
		Req: wire.HttpRequest{
			Method:         "GET",
			URL:            target.URL + "/blocked",
			DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
		},
	}, 8*time.Second, 100*time.Millisecond)

	if reached {
		t.Fatal("target was reached despite host being disallowed")
	}
	if response.Meta.Error == nil {
		t.Fatalf("expected execution error, got response status %d", response.Status)
	}
	if response.Meta.Error.Kind != wire.ErrorKindHostNotAllowed {
		t.Fatalf("unexpected error kind: %s", response.Meta.Error.Kind)
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

func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	defer func() { _ = listener.Close() }()

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

func projectRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	return root
}
