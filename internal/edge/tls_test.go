package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

func tlsTarget(t *testing.T, options certificateOptions, handler http.Handler, configure ...func(*http.Server)) (*httptest.Server, *x509.Certificate, *x509.CertPool) {
	t.Helper()
	root, key, roots := newTestCA(t)
	leaf, leafKey := newTestCertificateWithKey(t, root, key, options)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey}}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	for _, setup := range configure {
		setup(server.Config)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, leaf, roots
}

func pinnedRequest(target string, leaf *x509.Certificate) wire.HttpRequest {
	return wire.HttpRequest{
		Method: "POST", URL: target, DeadlineUnixMs: uint64(time.Now().Add(5 * time.Second).UnixMilli()),
		Headers:   []wire.HeaderEntry{{Name: "Authorization", Value: "Bearer secret"}},
		Body:      base64.StdEncoding.EncodeToString([]byte("secret-body")),
		TLSPolicy: &wire.TLSPolicy{Mode: wire.TLSModePinnedCertificate, Origin: target, CertificateSHA256: certificateFingerprint(leaf)},
	}
}

func requireCode(t *testing.T, response wire.HttpResponse, code string) {
	t.Helper()
	if response.Meta.Error == nil || response.Meta.Error.Code != code {
		t.Fatalf("error = %#v, want code %s", response.Meta.Error, code)
	}
}

func TestPinnedDispatchVerifiesBeforeHTTP(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		options  certificateOptions
		code     string
		wrongPin bool
	}{
		{name: "generic CN private CA", options: certificateOptions{commonName: "SplunkServerDefaultCert"}},
		{name: "absent EKU", options: certificateOptions{commonName: "legacy", usages: []x509.ExtKeyUsage{}}},
		{name: "wrong pin", wrongPin: true, code: "certificate_pin_mismatch"},
		{name: "expired", options: certificateOptions{notBefore: now.Add(-2 * time.Hour), notAfter: now.Add(-time.Hour)}, code: "certificate_expired"},
		{name: "future", options: certificateOptions{notBefore: now.Add(time.Hour), notAfter: now.Add(2 * time.Hour)}, code: "certificate_not_yet_valid"},
		{name: "client only", options: certificateOptions{usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, code: "certificate_wrong_usage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reached atomic.Int32
			server, leaf, _ := tlsTarget(t, test.options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "Bearer secret" || string(body) != "secret-body" {
					t.Error("credentials/body changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := pinnedRequest(server.URL, leaf)
			if test.wrongPin {
				request.TLSPolicy.CertificateSHA256 = strings.Repeat("0", 64)
			}
			response := newExecutor(nil, []string{"127.0.0.1"}).Execute("pin", request)
			if test.code != "" {
				requireCode(t, response, test.code)
				if reached.Load() != 0 {
					t.Fatal("HTTP credentials reached rejected server")
				}
			} else if response.Meta.Error != nil || response.Status != http.StatusNoContent || reached.Load() != 1 {
				t.Fatalf("pinned request failed: %#v", response)
			}
		})
	}
}

func TestPinnedDispatchIsolationAndRedirects(t *testing.T) {
	var reached, connections atomic.Int32
	server, leaf, _ := tlsTarget(t, certificateOptions{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), func(server *http.Server) {
		server.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		}
	})
	executor := newExecutor(nil, []string{"127.0.0.1"})
	good := pinnedRequest(server.URL, leaf)
	bad := pinnedRequest(server.URL, leaf)
	bad.TLSPolicy.CertificateSHA256 = strings.Repeat("0", 64)
	for i := 0; i < 2; i++ {
		if response := executor.Execute("good", good); response.Meta.Error != nil {
			t.Fatal(response.Meta.Error)
		}
		requireCode(t, executor.Execute("bad", bad), "certificate_pin_mismatch")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if response := executor.Execute("good", good); response.Meta.Error != nil {
					t.Error(response.Meta.Error)
				}
			} else {
				requireCode(t, executor.Execute("bad", bad), "certificate_pin_mismatch")
			}
		}(i)
	}
	wg.Wait()
	if reached.Load() != 6 || connections.Load() != 12 {
		t.Fatalf("requests=%d connections=%d", reached.Load(), connections.Load())
	}
	unpinned := good
	unpinned.TLSPolicy = nil
	if response := executor.Execute("strict", unpinned); response.Meta.Error == nil {
		t.Fatal("pinning weakened default trust")
	}
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"/again", server.URL, "http://127.0.0.1:1", "https://elsewhere.example"} {
			t.Run(strconv.Itoa(code)+destination, func(t *testing.T) {
				before := reached.Load()
				redirect, cert, _ := tlsTarget(t, certificateOptions{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, destination, code)
				}))
				requireCode(t, executor.Execute("redirect", pinnedRequest(redirect.URL, cert)), "redirect_rejected")
				if reached.Load() != before {
					t.Fatal("redirect sent another request")
				}
			})
		}
	}
}

func probeRequest(server *httptest.Server) wire.TLSPreflightRequest {
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return wire.TLSPreflightRequest{Hostname: host, Port: uint16(number), DeadlineUnixMs: uint64(time.Now().Add(5 * time.Second).UnixMilli())}
}

func TestPreflightIndependentDiagnosticsAndNoHTTP(t *testing.T) {
	var reached atomic.Int32
	server, leaf, _ := tlsTarget(t, certificateOptions{commonName: "SplunkServerDefaultCert"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	executor := newExecutor(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}, []string{"127.0.0.1"})
	response := executor.Preflight(context.Background(), "probe", probeRequest(server))
	if response.Meta.Error != nil {
		t.Fatal(response.Meta.Error)
	}
	p := response.Preflight
	if p.Route != "direct" || p.DNS.Status != "passed" || p.TCP.Status != "passed" || p.TLS.Status != "passed" {
		t.Fatalf("reachability: %#v", p)
	}
	if p.Hostname.Code != "hostname_mismatch" || p.Chain.Code != "unknown_authority" || p.Validity.Status != "passed" || p.Authenticated {
		t.Fatalf("verification: %#v", p)
	}
	if p.Certificate.CertificateSHA256 != certificateFingerprint(leaf) || p.Certificate.SANPresent {
		t.Fatalf("metadata: %#v", p.Certificate)
	}
	if reached.Load() != 0 {
		t.Fatal("preflight sent HTTP")
	}
	// A diagnostic fingerprint never authorizes the later authenticated connection.
	request := pinnedRequest(server.URL, leaf)
	request.TLSPolicy.CertificateSHA256 = strings.Repeat("0", 64)
	requireCode(t, executor.Execute("changed", request), "certificate_pin_mismatch")
	if reached.Load() != 0 {
		t.Fatal("failed pin sent HTTP after preflight")
	}
	requireCode(t, newExecutor(nil, nil).Preflight(context.Background(), "probe", probeRequest(server)), "host_not_allowed")
}

func TestPreflightTrustedLegacyAndExpired(t *testing.T) {
	for _, expired := range []bool{false, true} {
		options := certificateOptions{commonName: "localhost"}
		if expired {
			options.notBefore = time.Now().Add(-2 * time.Hour)
			options.notAfter = time.Now().Add(-time.Hour)
		}
		server, _, roots := tlsTarget(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }))
		request := probeRequest(server)
		request.Hostname = "localhost"
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
		response := newExecutor(client, []string{"localhost"}).Preflight(context.Background(), "legacy", request)
		if response.Meta.Error != nil {
			t.Fatal(response.Meta.Error)
		}
		p := response.Preflight
		if p.Hostname.Code != "hostname_mismatch" {
			t.Fatalf("hostname=%#v", p.Hostname)
		}
		if expired {
			if p.Validity.Code != "certificate_expired" || p.Chain.Code != "certificate_expired" || p.Authenticated {
				t.Fatalf("expired: %#v", p)
			}
		} else if !p.LegacyCNFallbackApplied || !p.Authenticated || p.Verification.Status != "passed" {
			t.Fatalf("legacy: %#v", p)
		}
	}
}

func TestPreflightDeadlineAndConnectionFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	request := wire.TLSPreflightRequest{Hostname: "127.0.0.1", Port: uint16(address.Port), DeadlineUnixMs: uint64(time.Now().Add(100 * time.Millisecond).UnixMilli())}
	executor := newExecutor(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}, []string{"127.0.0.1"})
	response := executor.Preflight(context.Background(), "timeout", request)
	requireCode(t, response, "timeout")
	if response.Preflight.TCP.Status != "passed" || response.Preflight.TLS.Status != "failed" || response.Preflight.Chain.Status != "not_checked" {
		t.Fatalf("timeout stages: %#v", response.Preflight)
	}
	<-done
	_ = listener.Close()
	request.DeadlineUnixMs = uint64(time.Now().Add(time.Second).UnixMilli())
	response = executor.Preflight(context.Background(), "refused", request)
	requireCode(t, response, "connection_refused")
	if response.Preflight.TLS.Status != "not_checked" {
		t.Fatal("TLS attempted after TCP failure")
	}
}

func TestCertificateRotationAfterPreflight(t *testing.T) {
	root, key, _ := newTestCA(t)
	first, firstKey := newTestCertificateWithKey(t, root, key, certificateOptions{})
	second, secondKey := newTestCertificateWithKey(t, root, key, certificateOptions{})
	var current atomic.Pointer[tls.Certificate]
	current.Store(&tls.Certificate{Certificate: [][]byte{first.Raw}, PrivateKey: firstKey})
	var reached atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	server.TLS = &tls.Config{GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) { return current.Load(), nil }}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	// httptest installs a default certificate if Certificates is empty. A DNS
	// target supplies SNI so the explicitly configured callback is used.
	request := probeRequest(server)
	request.Hostname = "localhost"
	executor := newExecutor(nil, []string{"localhost"})
	probe := executor.Preflight(context.Background(), "rotation", request)
	if probe.Meta.Error != nil || probe.Preflight.Certificate.CertificateSHA256 != certificateFingerprint(first) {
		t.Fatalf("probe: %#v", probe)
	}
	current.Store(&tls.Certificate{Certificate: [][]byte{second.Raw}, PrivateKey: secondKey})
	target := "https://localhost:" + strconv.Itoa(int(request.Port))
	requireCode(t, executor.Execute("rotation", pinnedRequest(target, first)), "certificate_pin_mismatch")
	if reached.Load() != 0 {
		t.Fatal("HTTP sent after unexpected rotation")
	}
}

func TestPreflightBoundsMetadataAndSendsNoClientCertificate(t *testing.T) {
	root, key, _ := newTestCA(t)
	names := make([]string, 2000)
	for i := range names {
		names[i] = strings.Repeat("a", 40) + strconv.Itoa(i) + ".example"
	}
	leaf, leafKey := newTestCertificateWithKey(t, root, key, certificateOptions{dnsNames: names})
	var clientCertificates atomic.Int32
	var reached atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	server.TLS = &tls.Config{
		Certificates:          []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey}},
		ClientAuth:            tls.RequestClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error { clientCertificates.Add(int32(len(raw))); return nil },
	}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	// Even an injected base client with client credentials must not supply them
	// on the diagnostic connection.
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey}},
	}}}
	response := newExecutor(base, []string{"127.0.0.1"}).Preflight(context.Background(), "large", probeRequest(server))
	requireCode(t, response, "diagnostics_too_large")
	if response.Preflight.Certificate != nil || reached.Load() != 0 || clientCertificates.Load() != 0 {
		t.Fatal("diagnostics leaked application data or exceeded metadata bound")
	}
}

func TestPinnedRequestRejectsInvalidPolicyBeforeDial(t *testing.T) {
	var dials atomic.Int32
	base := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return nil, context.Canceled
	}}}
	root, key, _ := newTestCA(t)
	leaf := newTestCertificate(t, root, key, certificateOptions{})
	for _, test := range []struct {
		name              string
		hosts             []string
		mode, origin, pin string
		code              string
	}{
		{"no allowlist", nil, wire.TLSModePinnedCertificate, "https://target.example", certificateFingerprint(leaf), "host_not_allowed"},
		{"disallowed", []string{"elsewhere.example"}, wire.TLSModePinnedCertificate, "https://target.example", certificateFingerprint(leaf), "host_not_allowed"},
		{"mode", []string{"target.example"}, "insecure", "https://target.example", certificateFingerprint(leaf), "invalid_request"},
		{"origin", []string{"target.example"}, wire.TLSModePinnedCertificate, "https://target.example:444", certificateFingerprint(leaf), "invalid_request"},
		{"pin", []string{"target.example"}, wire.TLSModePinnedCertificate, "https://target.example", "invalid", "invalid_request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := pinnedRequest("https://target.example", leaf)
			request.TLSPolicy = &wire.TLSPolicy{Mode: test.mode, Origin: test.origin, CertificateSHA256: test.pin}
			requireCode(t, newExecutor(base, test.hosts).Execute("invalid", request), test.code)
		})
	}
	if dials.Load() != 0 {
		t.Fatal("invalid policy triggered network activity")
	}
}
