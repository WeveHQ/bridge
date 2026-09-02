package edge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

func TestVerifyTargetCertificate(t *testing.T) {
	now := time.Now()
	root, rootKey, roots := newTestCA(t)

	tests := []struct {
		name         string
		hostname     string
		allowedHosts []string
		certificate  certificateOptions
		roots        *x509.CertPool
		wantLegacy   bool
		wantError    bool
	}{
		{
			name:         "valid SAN certificate uses normal verification",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "ignored.example", dnsNames: []string{"target.example"}},
			roots:        roots,
		},
		{
			name:         "allowlisted CN-only certificate",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example"},
			roots:        roots,
			wantLegacy:   true,
		},
		{
			name:        "CN-only certificate without allowlist",
			hostname:    "target.example",
			certificate: certificateOptions{commonName: "target.example"},
			roots:       roots,
			wantError:   true,
		},
		{
			name:         "untrusted CN-only certificate",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example"},
			roots:        x509.NewCertPool(),
			wantError:    true,
		},
		{
			name:         "expired CN-only certificate",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example", notBefore: now.Add(-2 * time.Hour), notAfter: now.Add(-time.Hour)},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "not-yet-valid CN-only certificate",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example", notBefore: now.Add(time.Hour), notAfter: now.Add(2 * time.Hour)},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "wrong usage CN-only certificate",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example", usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "wrong CN",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "other.example"},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "wildcard CN",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "*.example"},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "IP-address CN",
			hostname:     "127.0.0.1",
			allowedHosts: []string{"127.0.0.1"},
			certificate:  certificateOptions{commonName: "127.0.0.1"},
			roots:        roots,
			wantError:    true,
		},
		{
			name:         "mismatching SAN does not fall back to CN",
			hostname:     "target.example",
			allowedHosts: []string{"target.example"},
			certificate:  certificateOptions{commonName: "target.example", dnsNames: []string{"other.example"}},
			roots:        roots,
			wantError:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			leaf := newTestCertificate(t, root, rootKey, test.certificate)
			chains, legacy, err := verifyTargetCertificate(tls.ConnectionState{
				ServerName:       test.hostname,
				PeerCertificates: []*x509.Certificate{leaf},
			}, test.roots, test.allowedHosts)

			if test.wantError {
				if err == nil {
					t.Fatal("expected verification error")
				}
				return
			}
			if err != nil {
				t.Fatalf("verify certificate: %v", err)
			}
			if len(chains) == 0 {
				t.Fatal("expected a verified chain")
			}
			if legacy != test.wantLegacy {
				t.Fatalf("legacy fallback = %t, want %t", legacy, test.wantLegacy)
			}
		})
	}
}

func TestExecutorConnectsToAllowlistedCNOnlyServer(t *testing.T) {
	root, rootKey, roots := newTestCA(t)
	leaf, leafKey := newTestCertificateWithKey(t, root, rootKey, certificateOptions{commonName: "target.example"})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{leaf.Raw},
		PrivateKey:  leafKey,
	}}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	targetAddress := server.Listener.Addr().String()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, targetAddress)
		},
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	request := wire.HttpRequest{
		Method:         http.MethodGet,
		URL:            "https://target.example/",
		DeadlineUnixMs: uint64(time.Now().Add(time.Minute).UnixMilli()),
	}

	allowedResponse := newExecutor(client, []string{"target.example"}).Execute("ot_allowed", request)
	if allowedResponse.Meta.Error != nil {
		t.Fatalf("allowlisted CN-only request failed: %#v", allowedResponse.Meta.Error)
	}
	if allowedResponse.Status != http.StatusNoContent {
		t.Fatalf("allowlisted status = %d, want %d", allowedResponse.Status, http.StatusNoContent)
	}

	strictResponse := newExecutor(client, nil).Execute("ot_strict", request)
	if strictResponse.Meta.Error == nil {
		t.Fatal("CN-only request unexpectedly succeeded without an allowlist")
	}
	if strictResponse.Meta.Error.Kind != wire.ErrorKindTLS {
		t.Fatalf("strict error kind = %q, want %q", strictResponse.Meta.Error.Kind, wire.ErrorKindTLS)
	}
}

func TestTargetClientRejectsRedirectToDisallowedHost(t *testing.T) {
	client := newTargetClient(nil, []string{"allowed.example"})
	redirectURL, err := url.Parse("https://other.example/path")
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	err = client.CheckRedirect(&http.Request{URL: redirectURL}, []*http.Request{{}})
	if err == nil {
		t.Fatal("expected redirect to disallowed host to fail")
	}
	if kind := mapErrorKind(err); kind != "host_not_allowed" {
		t.Fatalf("error kind = %q, want host_not_allowed", kind)
	}
}

func TestRunnerKeepsHubTLSStrict(t *testing.T) {
	hubTransport := &http.Transport{TLSClientConfig: &tls.Config{}}
	hubClient := &http.Client{Transport: hubTransport}
	runner := NewRunner(Config{
		Client:       hubClient,
		AllowedHosts: []string{"target.example"},
	})

	if runner.hubClient != hubClient {
		t.Fatal("runner did not preserve the hub client")
	}
	if hubTransport.TLSClientConfig.InsecureSkipVerify || hubTransport.TLSClientConfig.VerifyConnection != nil {
		t.Fatal("target compatibility verifier leaked into hub transport")
	}
	targetTransport, ok := runner.executor.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected target transport type %T", runner.executor.client.Transport)
	}
	if targetTransport == hubTransport || targetTransport.TLSClientConfig == hubTransport.TLSClientConfig {
		t.Fatal("hub and target transports must be separate")
	}
	if !targetTransport.TLSClientConfig.InsecureSkipVerify || targetTransport.TLSClientConfig.VerifyConnection == nil {
		t.Fatal("target transport is missing the compatibility verifier")
	}
}

type certificateOptions struct {
	commonName string
	dnsNames   []string
	notBefore  time.Time
	notAfter   time.Time
	usages     []x509.ExtKeyUsage
}

func newTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return certificate, key, roots
}

func newTestCertificate(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey, options certificateOptions) *x509.Certificate {
	t.Helper()
	certificate, _ := newTestCertificateWithKey(t, root, rootKey, options)
	return certificate
}

func newTestCertificateWithKey(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey, options certificateOptions) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	if options.notBefore.IsZero() {
		options.notBefore = time.Now().Add(-time.Hour)
	}
	if options.notAfter.IsZero() {
		options.notAfter = time.Now().Add(time.Hour)
	}
	if options.usages == nil {
		options.usages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: options.commonName},
		DNSNames:     options.dnsNames,
		NotBefore:    options.notBefore,
		NotAfter:     options.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  options.usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}
	return certificate, key
}
