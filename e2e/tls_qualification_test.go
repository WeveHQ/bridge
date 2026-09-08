//go:build docker

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/testsupport"
	"github.com/WeveHQ/bridge/internal/wire"
)

type labState struct {
	Endpoints map[string]struct {
		Fingerprint string                                        `json:"fingerprint"`
		Counters    struct{ Requests, Authorized, BodyBytes int } `json:"counters"`
	} `json:"endpoints"`
	DowngradeRequests int `json:"downgradeRequests"`
}

func (state labState) requests() int {
	total := state.DowngradeRequests
	for _, endpoint := range state.Endpoints {
		total += endpoint.Counters.Requests
	}
	return total
}

func TestTenantTLSQualification(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker daemon required")
	}
	project := "bridge-tls-lab-" + fmt.Sprint(time.Now().UnixNano())
	hubPort, adminPort := allocatePort(t), allocatePort(t)
	compose := func(args ...string) ([]byte, error) {
		command := exec.Command("docker", append([]string{"compose", "-f", "e2e/tls-lab/compose.yml", "-p", project}, args...)...)
		command.Dir = repoRoot(t)
		command.Env = append(os.Environ(), fmt.Sprintf("TLS_LAB_HUB_PORT=%d", hubPort), fmt.Sprintf("TLS_LAB_ADMIN_PORT=%d", adminPort))
		return command.CombinedOutput()
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := compose("logs", "--no-color")
			t.Log(string(logs))
		}
		if output, err := compose("down", "-v", "--remove-orphans"); err != nil {
			t.Errorf("cleanup: %v %s", err, output)
		}
	})
	if output, err := compose("up", "-d", "--build"); err != nil {
		t.Fatalf("start lab: %v\n%s", err, output)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	testsupport.WaitForHub(t, baseURL, 20*time.Second, 100*time.Millisecond)
	client := &http.Client{Timeout: 5 * time.Second}
	state := func() labState {
		response, err := client.Get(adminURL + "/state")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var value labState
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	dispatch := func(request wire.DispatchRequest) wire.HttpResponse {
		request.OutboundTraceID = fmt.Sprintf("qualification-%d", time.Now().UnixNano())
		return testsupport.DispatchWithRetry(t, baseURL, "bridge_123", "internal-secret", request, 20*time.Second, 200*time.Millisecond)
	}
	probe := func(host string, port uint16) wire.HttpResponse {
		return dispatch(wire.DispatchRequest{Operation: wire.OperationTLSPreflight, Preflight: &wire.TLSPreflightRequest{
			Hostname: host, Port: port, DeadlineUnixMs: uint64(time.Now().Add(20 * time.Second).UnixMilli()),
		}})
	}
	httpRequest := func(port uint16, pin string) wire.HttpRequest {
		origin := fmt.Sprintf("https://tls-target:%d", port)
		req := wire.HttpRequest{Method: "POST", URL: origin + "/services/server/info", DeadlineUnixMs: uint64(time.Now().Add(20 * time.Second).UnixMilli()), Headers: []wire.HeaderEntry{{Name: "Authorization", Value: "Bearer simulated-connector-token"}}, Body: base64.StdEncoding.EncodeToString([]byte("simulated request body"))}
		if pin != "" {
			req.TLSPolicy = &wire.TLSPolicy{Mode: wire.TLSModePinnedCertificate, Origin: origin, CertificateSHA256: pin}
		}
		return req
	}
	t.Run("hub cannot directly reach tenant target", func(t *testing.T) {
		if output, err := compose("exec", "-T", "hub", "wget", "-T", "2", "-qO-", "http://tls-target:8080"); err == nil {
			t.Fatalf("hub unexpectedly reached private target: %s", output)
		}
		if state().requests() != 0 {
			t.Fatal("direct hub probe reached target")
		}
		t.Log("hub has no direct target access; requests must traverse the edge")
	})
	initial := state()
	for _, test := range []struct {
		name                      string
		port                      uint16
		hostname, chain, validity string
		authenticated, legacy     bool
	}{
		{"strict", 8443, "passed", "passed", "passed", true, false},
		{"legacy", 8444, "failed", "passed", "passed", true, true},
		{"generic", 8445, "failed", "failed", "passed", false, false},
		{"expired", 8446, "failed", "failed", "failed", false, false},
		{"future", 8447, "failed", "failed", "failed", false, false},
	} {
		t.Run("preflight_"+test.name, func(t *testing.T) {
			before := state().requests()
			response := probe("tls-target", test.port)
			p := response.Preflight
			if response.Meta.Error != nil || p == nil {
				t.Fatalf("preflight failed: %#v", response)
			}
			if p.DNS.Status != "passed" || p.TCP.Status != "passed" || p.TLS.Status != "passed" || p.Hostname.Status != test.hostname || p.Chain.Status != test.chain || p.Validity.Status != test.validity || p.Authenticated != test.authenticated || p.LegacyCNFallbackApplied != test.legacy {
				t.Fatalf("unexpected diagnostics: %#v", p)
			}
			if p.Certificate.CertificateSHA256 != initial.Endpoints[test.name].Fingerprint {
				t.Fatal("fingerprint differs from server-owned certificate")
			}
			if state().requests() != before {
				t.Fatal("preflight sent an HTTP request")
			}
			t.Logf("hostname=%s chain=%s validity=%s authenticated=%t; target HTTP requests=0", p.Hostname.Status, p.Chain.Status, p.Validity.Status, p.Authenticated)
		})
	}
	for _, test := range []struct {
		name, host string
		port       uint16
		code       string
	}{
		{"DNS failure", "missing.invalid", 8443, "dns"},
		{"wrong port", "tls-target", 8999, "connection_refused"},
		{"disallowed host", "verifier", 8081, "host_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := state().requests()
			response := probe(test.host, test.port)
			if response.Meta.Error == nil || response.Meta.Error.Code != test.code {
				t.Fatalf("error=%#v", response.Meta.Error)
			}
			if state().requests() != before {
				t.Fatal("rejected probe sent HTTP")
			}
			t.Logf("code=%s; target HTTP requests=0", test.code)
		})
	}
	for _, test := range []struct {
		name, endpoint string
		port           uint16
		pin, code      string
	}{
		{"ordinary strict", "strict", 8443, "", ""},
		{"ordinary legacy CN", "legacy", 8444, "", ""},
		{"generic without pin", "generic", 8445, "", "hostname_mismatch"},
		{"approved generic pin", "generic", 8445, initial.Endpoints["generic"].Fingerprint, ""},
		{"wrong pin", "generic", 8445, strings.Repeat("0", 64), "certificate_pin_mismatch"},
		{"expired pin", "expired", 8446, initial.Endpoints["expired"].Fingerprint, "certificate_expired"},
		{"future pin", "future", 8447, initial.Endpoints["future"].Fingerprint, "certificate_not_yet_valid"},
		{"wrong usage pin", "wrong_usage", 8448, initial.Endpoints["wrong_usage"].Fingerprint, "certificate_wrong_usage"},
		{"downgrade redirect", "redirect", 8449, initial.Endpoints["redirect"].Fingerprint, "redirect_rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := state()
			response := dispatch(wire.DispatchRequest{Req: httpRequest(test.port, test.pin)})
			expected := 0
			if test.code == "" {
				expected = 1
				if response.Meta.Error != nil || response.Status != 204 {
					t.Fatalf("request failed: %#v", response)
				}
			} else {
				if response.Meta.Error == nil || response.Meta.Error.Code != test.code {
					t.Fatalf("error=%#v want %s", response.Meta.Error, test.code)
				}
				if test.code == "redirect_rejected" {
					expected = 1
				}
			}
			after := state()
			if after.requests()-before.requests() != expected || after.DowngradeRequests != 0 {
				t.Fatalf("unexpected HTTP traffic: before=%+v after=%+v", before, after)
			}
			if after.Endpoints[test.endpoint].Counters.Authorized-before.Endpoints[test.endpoint].Counters.Authorized != expected {
				t.Fatal("unexpected credential delivery")
			}
			t.Logf("code=%s; authenticated target HTTP requests=%d; downgrade requests=0", test.code, expected)
		})
	}
	t.Run("certificate rotation", func(t *testing.T) {
		before := state().requests()
		response, err := client.Post(adminURL+"/rotate", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatal("rotation failed")
		}
		rejected := dispatch(wire.DispatchRequest{Req: httpRequest(8445, initial.Endpoints["generic"].Fingerprint)})
		if rejected.Meta.Error == nil || rejected.Meta.Error.Code != "certificate_pin_mismatch" || state().requests() != before {
			t.Fatalf("old pin did not fail closed: %#v", rejected)
		}
		// Approval comes from the fixture's admin inventory, independently of preflight.
		approved := state().Endpoints["generic"].Fingerprint
		if approved == initial.Endpoints["generic"].Fingerprint {
			t.Fatal("certificate did not change")
		}
		accepted := dispatch(wire.DispatchRequest{Req: httpRequest(8445, approved)})
		if accepted.Meta.Error != nil || accepted.Status != 204 || state().requests() != before+1 {
			t.Fatalf("new pin failed: %#v", accepted)
		}
		t.Log("old pin: zero HTTP requests; independently approved replacement: one HTTP request")
	})
}
