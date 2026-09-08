package edge

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

type codedError struct {
	kind    wire.ErrorKind
	code    string
	message string
}

func (err codedError) Error() string { return err.message }
func invalidRequest(err error) error {
	return codedError{wire.ErrorKindUnknown, "invalid_request", err.Error()}
}
func tlsFailure(code, message string) error { return codedError{wire.ErrorKindTLS, code, message} }

func certificateFingerprint(leaf *x509.Certificate) string {
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:])
}

func certificateValidity(leaf *x509.Certificate, now time.Time) error {
	if now.Before(leaf.NotBefore) {
		return tlsFailure("certificate_not_yet_valid", "certificate is not yet valid")
	}
	if now.After(leaf.NotAfter) {
		return tlsFailure("certificate_expired", "certificate has expired")
	}
	return nil
}

func verifyPinnedCertificate(state tls.ConnectionState, expected string) error {
	if len(state.PeerCertificates) == 0 {
		return tlsFailure("certificate_missing", "server did not provide a certificate")
	}
	leaf := state.PeerCertificates[0]
	if certificateFingerprint(leaf) != expected {
		return tlsFailure("certificate_pin_mismatch", "server certificate does not match the configured pin")
	}
	if err := certificateValidity(leaf, time.Now()); err != nil {
		return err
	}
	if len(leaf.UnhandledCriticalExtensions) != 0 {
		return tlsFailure("certificate_invalid", "certificate has unsupported critical extensions")
	}
	// TLS handshakes check possession of the private key. When a key-usage
	// extension is present, require signing permission for modern TLS.
	if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return tlsFailure("certificate_wrong_usage", "certificate does not permit TLS signing")
	}
	if len(leaf.ExtKeyUsage) != 0 || len(leaf.UnknownExtKeyUsage) != 0 {
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				return nil
			}
		}
		return tlsFailure("certificate_wrong_usage", "certificate does not permit TLS server authentication")
	}
	return nil
}

func (executor *executor) pinnedClient(policy *wire.TLSPolicy) (*http.Client, error) {
	base, ok := executor.client.Transport.(*http.Transport)
	if !ok || base.DialTLSContext != nil || base.DialTLS != nil {
		return nil, invalidRequest(errors.New("custom TLS transport cannot enforce certificate pinning"))
	}
	transport := base.Clone()
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The explicitly configured certificate pin replaces CA/name verification.
		InsecureSkipVerify: true, //nolint:gosec -- verified below before HTTP is sent
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyPinnedCertificate(state, policy.CertificateSHA256)
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return codedError{wire.ErrorKindUnknown, "redirect_rejected", "pinned requests do not follow redirects"}
		},
	}, nil
}

// errorCode returns stable values without exposing URLs or parsing x509 text.
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	var name x509.HostnameError
	if errors.As(err, &name) {
		return "hostname_mismatch"
	}
	var authority x509.UnknownAuthorityError
	if errors.As(err, &authority) {
		return "unknown_authority"
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		switch invalid.Reason {
		case x509.Expired:
			if invalid.Cert != nil && time.Now().Before(invalid.Cert.NotBefore) {
				return "certificate_not_yet_valid"
			}
			return "certificate_expired"
		case x509.IncompatibleUsage:
			return "certificate_wrong_usage"
		default:
			return "certificate_invalid"
		}
	}
	var critical x509.UnhandledCriticalExtension
	if errors.As(err, &critical) {
		return "certificate_invalid"
	}
	switch mapErrorKind(err) {
	case wire.ErrorKindTLS:
		return "tls_handshake_failed"
	case wire.ErrorKindUnknown:
		return "execution_failed"
	default:
		return string(mapErrorKind(err))
	}
}

func safeErrorMessage(err error) string {
	// net/http wraps failures with the full request URL, which can contain secrets.
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return safeErrorMessage(urlError.Err)
	}
	return err.Error()
}
