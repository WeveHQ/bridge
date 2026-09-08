package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

const preflightTimeout = 10 * time.Second

func unchecked() wire.TLSCheck { return wire.TLSCheck{Status: "not_checked"} }
func checkResult(err error) wire.TLSCheck {
	if err == nil {
		return wire.TLSCheck{Status: "passed"}
	}
	return wire.TLSCheck{Status: "failed", Code: errorCode(err)}
}

// Platform verifiers may return opaque errors. Keep these classified as
// certificate verification failures, not TLS protocol failures.
func certificateCheck(err error) wire.TLSCheck {
	result := checkResult(err)
	if err != nil && (result.Code == "tls_handshake_failed" || result.Code == "execution_failed") {
		result.Code = "certificate_invalid"
	}
	return result
}

func (executor *executor) Preflight(ctx context.Context, trace string, request wire.TLSPreflightRequest) wire.HttpResponse {
	started := time.Now()
	result := &wire.TLSPreflightResult{
		Route: "direct", DNS: unchecked(), TCP: unchecked(), TLS: unchecked(),
		Hostname: unchecked(), Chain: unchecked(), Validity: unchecked(), Verification: unchecked(),
	}
	finish := func(err error) wire.HttpResponse {
		response := wire.HttpResponse{OutboundTraceID: trace, Meta: wire.ExecutionMeta{
			StartedAtUnixMs: uint64(started.UnixMilli()), DurationMs: uint32(time.Since(started).Milliseconds()),
		}}
		if err != nil {
			response = newErrorResponse(trace, started, 0, err)
		}
		// Certificate subjects and SANs are peer-controlled. Never relay an unbounded
		// diagnostic payload or truncate a fingerprint into something misleading.
		encoded, _ := json.Marshal(result)
		if len(encoded) > wire.MaxPreflightResultBytes {
			result.Certificate = nil
			response = newErrorResponse(trace, started, 0, codedError{wire.ErrorKindTLS, "diagnostics_too_large", "certificate diagnostics exceed size limit"})
		}
		response.Preflight = result
		return response
	}
	if err := (wire.DispatchRequest{Operation: wire.OperationTLSPreflight, Preflight: &request}).Validate(); err != nil {
		return finish(invalidRequest(err))
	}
	hostname := strings.ToLower(request.Hostname)
	if !hostAllowed(hostname, executor.allowedHosts) {
		return finish(hostNotAllowedError{host: hostname})
	}
	deadline := started.Add(preflightTimeout)
	if requested := time.UnixMilli(int64(request.DeadlineUnixMs)); requested.Before(deadline) {
		deadline = requested
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	result.DNS = checkResult(err)
	if err != nil {
		return finish(err)
	}
	if len(addresses) == 0 {
		err = &net.DNSError{Err: "no addresses", Name: hostname, IsNotFound: true}
		result.DNS = checkResult(err)
		return finish(err)
	}
	var connection net.Conn
	for _, address := range addresses {
		connection, err = (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(int(request.Port))))
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	result.TCP = checkResult(err)
	if err != nil {
		return finish(err)
	}
	defer func() { _ = connection.Close() }()

	// This connection only collects public TLS metadata; no HTTP transport,
	// client authentication, session cache, or application writes are involved.
	tlsConnection := tls.Client(connection, &tls.Config{
		ServerName:         hostname,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // diagnostic-only, verified independently below
	})
	err = tlsConnection.HandshakeContext(ctx)
	if err != nil && mapErrorKind(err) == wire.ErrorKindUnknown {
		err = tlsFailure("tls_handshake_failed", "target TLS handshake failed")
	}
	result.TLS = checkResult(err)
	state := tlsConnection.ConnectionState()
	if err != nil {
		return finish(err)
	}
	if len(state.PeerCertificates) == 0 {
		err = tlsFailure("certificate_missing", "server did not provide a certificate")
		result.TLS = checkResult(err)
		return finish(err)
	}
	var roots *x509.CertPool
	if transport, ok := executor.client.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil {
		roots = transport.TLSClientConfig.RootCAs
	}
	leaf := state.PeerCertificates[0]
	result.Certificate = &wire.TLSCertificate{
		Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(),
		DNSNames: append([]string{}, leaf.DNSNames...), IPAddresses: []string{},
		SANPresent: certificateHasSAN(leaf),
		NotBefore:  leaf.NotBefore.UTC().Format(time.RFC3339), NotAfter: leaf.NotAfter.UTC().Format(time.RFC3339),
		CertificateSHA256: certificateFingerprint(leaf),
	}
	for _, ip := range leaf.IPAddresses {
		result.Certificate.IPAddresses = append(result.Certificate.IPAddresses, ip.String())
	}
	result.Hostname = checkResult(leaf.VerifyHostname(hostname))
	result.Validity = checkResult(certificateValidity(leaf, time.Now()))
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	_, chainErr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	result.Chain = certificateCheck(chainErr)
	// ConnectionState.ServerName is SNI and is empty for IP literals; verification
	// must instead use the requested identity, including IP SANs.
	state.ServerName = hostname
	_, legacy, verifyErr := verifyTargetCertificate(state, roots, executor.allowedHosts)
	result.Verification = certificateCheck(verifyErr)
	result.LegacyCNFallbackApplied = legacy
	result.Authenticated = verifyErr == nil
	return finish(nil)
}
