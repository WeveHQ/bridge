//go:build docker

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
	waitForHub(t, baseURL)
	runCompose(t, projectName, hubPort, token, "up", "-d", "--build", "edge")

	response := dispatchWithRetry(t, baseURL, wire.DispatchRequest{
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
	})

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
		"WEVE_BRIDGE_TOKEN="+token,
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
		"WEVE_BRIDGE_TOKEN="+token,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output)
	}

	return string(output)
}

func waitForHub(t *testing.T, baseURL string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
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

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatal("timed out waiting for hub")
}

func dispatchWithRetry(
	t *testing.T,
	baseURL string,
	payload wire.DispatchRequest,
) wire.HttpResponse {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, reject, err := dispatchOnce(baseURL, payload)
		if err == nil {
			return response
		}
		if reject == nil || reject.Error.Code != wire.BridgeOfflineCode {
			t.Fatalf("dispatch failed: %v", err)
		}

		time.Sleep(250 * time.Millisecond)
	}

	t.Fatal("timed out waiting for edge to connect")
	return wire.HttpResponse{}
}

func dispatchOnce(
	baseURL string,
	payload wire.DispatchRequest,
) (wire.HttpResponse, *wire.DispatchReject, error) {
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

func allocatePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer listener.Close()

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
