package wire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	OperationHTTP            = "http"
	OperationTLSPreflight    = "tls_preflight"
	TLSModePinnedCertificate = "pinned_certificate"
	MaxPreflightResultBytes  = 64 * 1024
)

type TLSPolicy struct {
	Mode              string `json:"mode"`
	Origin            string `json:"origin"`
	CertificateSHA256 string `json:"certificateSha256"`
}

type TLSPreflightRequest struct {
	Hostname       string `json:"hostname"`
	Port           uint16 `json:"port"`
	DeadlineUnixMs uint64 `json:"deadlineUnixMs"`
}

// These payloads deliberately reject unknown fields: diagnostics cannot accept
// credentials, and a misspelled security policy must never silently weaken it.
func (request *DispatchRequest) UnmarshalJSON(data []byte) error {
	type plain DispatchRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Operation == OperationTLSPreflight {
		if err := decodeStrict(data, &decoded); err != nil {
			return err
		}
	}
	*request = DispatchRequest(decoded)
	return nil
}

func (p *TLSPreflightRequest) UnmarshalJSON(data []byte) error {
	type plain TLSPreflightRequest
	return decodeStrict(data, (*plain)(p))
}
func (p *TLSPolicy) UnmarshalJSON(data []byte) error {
	type plain TLSPolicy
	return decodeStrict(data, (*plain)(p))
}
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func (request DispatchRequest) Validate() error {
	switch request.Operation {
	case "", OperationHTTP:
		if request.Preflight != nil {
			return errors.New("preflight requires tls_preflight operation")
		}
		return request.Req.TLSPolicy.Validate(request.Req.URL)
	case OperationTLSPreflight:
		if request.Preflight == nil {
			return errors.New("missing preflight target")
		}
		req := request.Req
		if req.Method != "" || req.URL != "" || len(req.Headers) != 0 || req.Body != "" || req.DeadlineUnixMs != 0 || req.TLSPolicy != nil {
			return errors.New("preflight cannot contain an HTTP request")
		}
		p := request.Preflight
		if !validHostname(p.Hostname) || p.Port == 0 || p.DeadlineUnixMs == 0 || p.DeadlineUnixMs > 1<<63-1 {
			return errors.New("invalid preflight hostname, port, or deadline")
		}
		return nil
	default:
		return errors.New("unknown dispatch operation")
	}
}

func (policy *TLSPolicy) Validate(target string) error {
	if policy == nil {
		return nil
	}
	if policy.Mode != TLSModePinnedCertificate {
		return errors.New("unknown TLS policy mode")
	}
	decoded, err := hex.DecodeString(policy.CertificateSHA256)
	if err != nil || len(decoded) != 32 || strings.ToLower(policy.CertificateSHA256) != policy.CertificateSHA256 {
		return errors.New("certificateSha256 must be 64 lowercase hexadecimal characters")
	}
	expected, err := HTTPSOrigin(policy.Origin)
	if err != nil {
		return errors.New("invalid TLS policy origin")
	}
	originURL, _ := url.Parse(policy.Origin)
	if originURL.Path != "" || originURL.RawQuery != "" || originURL.ForceQuery || originURL.Fragment != "" {
		return errors.New("TLS policy origin cannot contain a path, query, or fragment")
	}
	actual, err := HTTPSOrigin(target)
	if err != nil || actual != expected {
		return errors.New("pinned target must match the policy HTTPS origin")
	}
	return nil
}

func HTTPSOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || !validHostname(u.Hostname()) {
		return "", errors.New("invalid HTTPS origin")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return "", errors.New("invalid HTTPS port")
	}
	return "https://" + net.JoinHostPort(strings.ToLower(u.Hostname()), strconv.FormatUint(number, 10)), nil
}

func validHostname(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

type TLSCheck struct {
	Status string `json:"status"` // passed, failed, not_checked
	Code   string `json:"code,omitempty"`
}

type TLSCertificate struct {
	Subject           string   `json:"subject"`
	Issuer            string   `json:"issuer"`
	DNSNames          []string `json:"dnsNames"`
	IPAddresses       []string `json:"ipAddresses"`
	SANPresent        bool     `json:"sanPresent"`
	NotBefore         string   `json:"notBefore"`
	NotAfter          string   `json:"notAfter"`
	CertificateSHA256 string   `json:"certificateSha256"`
}

type TLSPreflightResult struct {
	Route                   string          `json:"route"`
	DNS                     TLSCheck        `json:"dns"`
	TCP                     TLSCheck        `json:"tcp"`
	TLS                     TLSCheck        `json:"tls"`
	Hostname                TLSCheck        `json:"hostname"`
	Chain                   TLSCheck        `json:"chain"`
	Validity                TLSCheck        `json:"validity"`
	Verification            TLSCheck        `json:"verification"`
	LegacyCNFallbackApplied bool            `json:"legacyCnFallbackApplied"`
	Authenticated           bool            `json:"authenticated"`
	Certificate             *TLSCertificate `json:"certificate,omitempty"`
}
