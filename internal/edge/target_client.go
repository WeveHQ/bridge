package edge

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

var subjectAlternativeNameOID = asn1.ObjectIdentifier{2, 5, 29, 17}

func newTargetClient(base *http.Client, allowedHosts []string) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if httpTransport, ok := transport.(*http.Transport); ok {
		targetTransport := httpTransport.Clone()
		if len(allowedHosts) > 0 {
			targetTransport.TLSClientConfig = legacyCommonNameTLSConfig(
				targetTransport.TLSClientConfig,
				allowedHosts,
			)
		}
		client.Transport = targetTransport
	}

	if len(allowedHosts) > 0 {
		previousCheckRedirect := client.CheckRedirect
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			host := strings.ToLower(request.URL.Hostname())
			if !hostAllowed(host, allowedHosts) {
				return hostNotAllowedError{host: host}
			}
			if previousCheckRedirect != nil {
				return previousCheckRedirect(request, via)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		}
	}

	return &client
}

// legacyCommonNameTLSConfig keeps all normal certificate checks in place and
// adds one narrow compatibility case for explicitly allowlisted target hosts.
func legacyCommonNameTLSConfig(base *tls.Config, allowedHosts []string) *tls.Config {
	config := &tls.Config{}
	if base != nil {
		config = base.Clone()
	}
	previousVerifyConnection := config.VerifyConnection

	// Verification is performed below with x509.Verify so that a missing SAN can
	// be handled after normal verification fails. Chain, time, and key-usage
	// checks are never skipped.
	config.InsecureSkipVerify = true //nolint:gosec -- replaced by the strict verifier below
	config.VerifyConnection = func(state tls.ConnectionState) error {
		chains, _, err := verifyTargetCertificate(state, config.RootCAs, allowedHosts)
		if err != nil {
			return err
		}
		state.VerifiedChains = chains

		if previousVerifyConnection != nil {
			if err := previousVerifyConnection(state); err != nil {
				return err
			}
		}

		return nil
	}

	return config
}

func verifyTargetCertificate(state tls.ConnectionState, roots *x509.CertPool, allowedHosts []string) ([][]*x509.Certificate, bool, error) {
	if len(state.PeerCertificates) == 0 {
		return nil, false, errors.New("tls: server did not provide a certificate")
	}

	leaf := state.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	options := x509.VerifyOptions{
		DNSName:       state.ServerName,
		Intermediates: intermediates,
		Roots:         roots,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	chains, err := leaf.Verify(options)
	if err == nil {
		return chains, false, nil
	}

	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) || certificateHasSAN(leaf) {
		return nil, false, err
	}

	hostname := strings.ToLower(state.ServerName)
	commonName := leaf.Subject.CommonName
	if !hostAllowed(hostname, allowedHosts) ||
		hostname == "" || net.ParseIP(hostname) != nil ||
		commonName == "" || strings.Contains(commonName, "*") || net.ParseIP(commonName) != nil ||
		!strings.EqualFold(commonName, hostname) {
		return nil, false, err
	}

	// Verify again without a DNS name. This preserves chain trust (including
	// SSL_CERT_FILE/system roots), validity dates, and TLS server-auth usage.
	options.DNSName = ""
	chains, err = leaf.Verify(options)
	if err != nil {
		return nil, false, err
	}
	return chains, true, nil
}

func certificateHasSAN(certificate *x509.Certificate) bool {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(subjectAlternativeNameOID) {
			return true
		}
	}
	return false
}

type hostNotAllowedError struct {
	host string
}

func (err hostNotAllowedError) Error() string {
	return fmt.Sprintf("host not allowed: %s", err.host)
}
