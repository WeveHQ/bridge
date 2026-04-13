package edge

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/WeveHQ/bridge/internal/wire"
)

func mapErrorKind(err error) wire.ErrorKind {
	if errors.Is(err, context.Canceled) {
		return wire.ErrorKindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return wire.ErrorKindTimeout
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return wire.ErrorKindDNS
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return wire.ErrorKindConnectionRefused
		}
		if errors.Is(opErr.Err, syscall.ECONNRESET) {
			return wire.ErrorKindConnectionReset
		}
	}

	var certErr x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return wire.ErrorKindTLS
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return wire.ErrorKindConnectionRefused
	case strings.Contains(message, "certificate"), strings.Contains(message, "tls"):
		return wire.ErrorKindTLS
	case strings.Contains(message, "reset"):
		return wire.ErrorKindConnectionReset
	}

	return wire.ErrorKindUnknown
}
