//go:build docker

package e2e

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/testsupport"
	"github.com/WeveHQ/bridge/internal/wire"
)

func TestDockerComposeRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	projectName := "weve-bridge-" + fmt.Sprint(time.Now().UnixNano())
	hubPort := allocatePort(t)
	token := "bridge-token"

	runCompose(t, projectName, hubPort, token, "up", "-d", "--build", "verifier", "target", "hub")
	defer runCompose(t, projectName, hubPort, token, "down", "-v", "--remove-orphans")

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	testsupport.WaitForHub(t, baseURL, 20*time.Second, 200*time.Millisecond)
	runCompose(t, projectName, hubPort, token, "up", "-d", "--build", "edge")

	response := testsupport.DispatchWithRetry(t, baseURL, "bridge_123", "internal-secret", wire.DispatchRequest{
		OutboundTraceID: "ot_docker",
		Req: wire.HttpRequest{
			Method:         http.MethodPost,
			URL:            "http://target:8080/search?q=docker",
			DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
			Headers: []wire.HeaderEntry{
				{Name: "Content-Type", Value: "application/json"},
			},
			Body: base64.StdEncoding.EncodeToString([]byte(`{"from":"docker"}`)),
		},
	}, 40*time.Second, 500*time.Millisecond)

	if response.Status != http.StatusCreated {
		t.Fatalf(
			"unexpected status: %d error=%#v logs=%s",
			response.Status,
			response.Meta.Error,
			composeLogs(t, projectName, hubPort, token),
		)
	}

	body, err := base64.StdEncoding.DecodeString(response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(body) != `{"source":"docker"}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestEdgeReconnectsAfterHubRestart(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	projectName := "weve-bridge-" + fmt.Sprint(time.Now().UnixNano())
	hubPort := allocatePort(t)
	token := "bridge-token"

	runCompose(t, projectName, hubPort, token, "up", "-d", "--build", "verifier", "target", "hub")
	defer runCompose(t, projectName, hubPort, token, "down", "-v", "--remove-orphans")

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	testsupport.WaitForHub(t, baseURL, 20*time.Second, 200*time.Millisecond)
	runCompose(t, projectName, hubPort, token, "up", "-d", "--build", "edge")

	// Verify the edge is working before we disrupt anything.
	response := testsupport.DispatchWithRetry(t, baseURL, "bridge_123", "internal-secret", wire.DispatchRequest{
		OutboundTraceID: "ot_before_restart",
		Req: wire.HttpRequest{
			Method:         http.MethodPost,
			URL:            "http://target:8080/search?q=before",
			DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
			Headers:        []wire.HeaderEntry{{Name: "Content-Type", Value: "application/json"}},
			Body:           base64.StdEncoding.EncodeToString([]byte(`{"phase":"before"}`)),
		},
	}, 40*time.Second, 500*time.Millisecond)
	if response.Status != http.StatusCreated {
		t.Fatalf("pre-restart dispatch failed: status=%d error=%#v", response.Status, response.Meta.Error)
	}

	// Kill the hub so the edge loses its connection.
	runCompose(t, projectName, hubPort, token, "stop", "hub")
	runCompose(t, projectName, hubPort, token, "rm", "-f", "hub")

	// Bring the hub back up on the same port.
	runCompose(t, projectName, hubPort, token, "up", "-d", "hub")
	testsupport.WaitForHub(t, baseURL, 20*time.Second, 200*time.Millisecond)

	// The edge should reconnect and handle a new dispatch.
	response = testsupport.DispatchWithRetry(t, baseURL, "bridge_123", "internal-secret", wire.DispatchRequest{
		OutboundTraceID: "ot_after_restart",
		Req: wire.HttpRequest{
			Method:         http.MethodPost,
			URL:            "http://target:8080/search?q=after",
			DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
			Headers:        []wire.HeaderEntry{{Name: "Content-Type", Value: "application/json"}},
			Body:           base64.StdEncoding.EncodeToString([]byte(`{"phase":"after"}`)),
		},
	}, 40*time.Second, 500*time.Millisecond)
	if response.Status != http.StatusCreated {
		t.Fatalf(
			"post-restart dispatch failed: status=%d error=%#v logs=%s",
			response.Status,
			response.Meta.Error,
			composeLogs(t, projectName, hubPort, token),
		)
	}
}

func runCompose(
	t *testing.T,
	projectName string,
	hubPort int,
	token string,
	args ...string,
) {
	t.Helper()

	command := exec.Command("docker", append([]string{
		"compose",
		"-f",
		"e2e/docker-compose.yml",
		"-p",
		projectName,
	}, args...)...)
	command.Dir = repoRoot(t)
	command.Env = append(os.Environ(),
		fmt.Sprintf("WEVE_BRIDGE_HUB_PORT=%d", hubPort),
		"WEVE_BRIDGE_EDGE_TOKEN="+token,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %v failed: %v\n%s", args, err, string(output))
	}
}

func composeLogs(
	t *testing.T,
	projectName string,
	hubPort int,
	token string,
) string {
	t.Helper()

	command := exec.Command(
		"docker",
		"compose",
		"-f",
		"e2e/docker-compose.yml",
		"-p",
		projectName,
		"logs",
	)
	command.Dir = repoRoot(t)
	command.Env = append(os.Environ(),
		fmt.Sprintf("WEVE_BRIDGE_HUB_PORT=%d", hubPort),
		"WEVE_BRIDGE_EDGE_TOKEN="+token,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output)
	}

	return string(output)
}

func allocatePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	return listener.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	return root
}
