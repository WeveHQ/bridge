package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/testsupport"
	"github.com/WeveHQ/bridge/internal/wire"
)

func TestBridgeBinaryTLSPreflightAndPinning(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.StartTLS()
	defer target.Close()
	binaryPath := buildBinary(t)
	token := "bridge-token"
	hubAddr := freeAddr(t)
	verifyURL := testsupport.StartVerifierServer(t, token, "verifier-secret")
	hubCmd := startProcess(t, binaryPath, []string{"hub", "--listen=" + hubAddr}, []string{
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL=" + verifyURL,
		"WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET=verifier-secret",
		"WEVE_BRIDGE_HUB_SECRET=internal-secret",
		"WEVE_BRIDGE_HUB_POLL_HOLD_SECONDS=1",
	})
	defer stopProcess(hubCmd)
	testsupport.WaitForHub(t, "http://"+hubAddr, 5*time.Second, 50*time.Millisecond)
	edgeCmd := startProcess(t, binaryPath, []string{"edge", "--token=" + token, "--hub-url=http://" + hubAddr}, []string{
		"WEVE_BRIDGE_EDGE_POLL_CONCURRENCY=2",
		"WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS=1",
		"WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS=1500",
		"WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=127.0.0.1",
	})
	defer stopProcess(edgeCmd)
	dispatch := func(req wire.DispatchRequest) wire.HttpResponse {
		return testsupport.DispatchWithRetry(t, "http://"+hubAddr, "bridge_123", "internal-secret", req, 8*time.Second, 100*time.Millisecond)
	}
	host, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	response := dispatch(wire.DispatchRequest{OutboundTraceID: "tls_probe", Operation: wire.OperationTLSPreflight, Preflight: &wire.TLSPreflightRequest{
		Hostname: host, Port: uint16(portNumber), DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
	}})
	if response.Meta.Error != nil || response.Preflight == nil || response.Preflight.TLS.Status != "passed" || response.Preflight.Authenticated {
		t.Fatalf("probe: %#v", response)
	}
	fingerprint := sha256.Sum256(target.Certificate().Raw)
	pin := hex.EncodeToString(fingerprint[:])
	if response.Preflight.Certificate.CertificateSHA256 != pin || reached.Load() != 0 {
		t.Fatal("preflight metadata incorrect or sent HTTP")
	}
	request := wire.HttpRequest{Method: "GET", URL: target.URL, DeadlineUnixMs: uint64(time.Now().Add(10 * time.Second).UnixMilli()),
		Headers:   []wire.HeaderEntry{{Name: "Authorization", Value: "Bearer secret"}},
		TLSPolicy: &wire.TLSPolicy{Mode: wire.TLSModePinnedCertificate, Origin: target.URL, CertificateSHA256: pin},
	}
	response = dispatch(wire.DispatchRequest{OutboundTraceID: "tls_pin", Req: request})
	if response.Meta.Error != nil || response.Status != http.StatusNoContent || reached.Load() != 1 {
		t.Fatalf("pin: %#v", response)
	}
	request.TLSPolicy.CertificateSHA256 = strings.Repeat("0", 64)
	response = dispatch(wire.DispatchRequest{OutboundTraceID: "tls_wrong_pin", Req: request})
	if response.Meta.Error == nil || response.Meta.Error.Code != "certificate_pin_mismatch" || reached.Load() != 1 {
		t.Fatalf("wrong pin: %#v", response)
	}
	_, reject, err := testsupport.DispatchOnce("http://"+hubAddr, "bridge_123", "internal-secret", wire.DispatchRequest{OutboundTraceID: "unknown", Operation: "unknown"})
	if err == nil || reject == nil || reject.Error.Code != "invalid_request" {
		t.Fatalf("invalid operation: %#v %v", reject, err)
	}
}
