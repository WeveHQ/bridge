package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTLSDispatchValidation(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		valid         bool
	}{
		{"ordinary", `{"req":{"method":"GET","url":"https://example.com"}}`, true},
		{"preflight", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345}}`, true},
		{"unknown operation", `{"operation":"tls_prefight"}`, false},
		{"missing target", `{"operation":"tls_preflight"}`, false},
		{"missing operation", `{"preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345}}`, false},
		{"headers", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345,"headers":[{"name":"Authorization","value":"secret"}]}}`, false},
		{"mixed request", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345},"req":{"body":"secret"}}`, false},
		{"outer credentials", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345},"authorization":"secret"}`, false},
		{"unknown request credentials", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":443,"deadlineUnixMs":12345},"req":{"authorization":"secret"}}`, false},
		{"URL hostname", `{"operation":"tls_preflight","preflight":{"hostname":"https://user:secret@example.com","port":443,"deadlineUnixMs":12345}}`, false},
		{"zero port", `{"operation":"tls_preflight","preflight":{"hostname":"example.com","port":0,"deadlineUnixMs":12345}}`, false},
		{"bad mode", `{"req":{"tlsPolicy":{"mode":"insecure"}}}`, false},
		{"unknown policy field", `{"req":{"tlsPolicy":{"skipTlsVerification":true}}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var req DispatchRequest
			err := json.Unmarshal([]byte(test.payload), &req)
			if err == nil {
				err = req.Validate()
			}
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
		})
	}
}

func TestPinOriginValidation(t *testing.T) {
	policy := TLSPolicy{Mode: TLSModePinnedCertificate, Origin: "https://EXAMPLE.com", CertificateSHA256: strings.Repeat("ab", 32)}
	for _, target := range []string{"https://example.com/path?query=1", "https://example.com:443/path"} {
		if err := policy.Validate(target); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"http://example.com", "https://other.com", "https://example.com:444", "https://user:secret@example.com", "https://example.com:0"} {
		if err := policy.Validate(target); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	for _, origin := range []string{"https://example.com/path", "https://example.com?x=1", "https://example.com/#fragment"} {
		copy := policy
		copy.Origin = origin
		if err := copy.Validate("https://example.com"); err == nil {
			t.Fatalf("accepted origin %s", origin)
		}
	}
	policy.CertificateSHA256 = strings.Repeat("AB", 32)
	if err := policy.Validate("https://example.com"); err == nil {
		t.Fatal("accepted uppercase pin")
	}
}

func TestTLSWireRoundTrip(t *testing.T) {
	for _, request := range []DispatchRequest{
		{Operation: OperationTLSPreflight, Preflight: &TLSPreflightRequest{Hostname: "localhost", Port: 443, DeadlineUnixMs: 12345}},
		{Req: HttpRequest{Method: "GET", URL: "https://localhost", TLSPolicy: &TLSPolicy{Mode: TLSModePinnedCertificate, Origin: "https://localhost", CertificateSHA256: strings.Repeat("a", 64)}}},
	} {
		data := MustJSON(request)
		var decoded PollResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatal(err)
		}
		if string(MustJSON(decoded)) != string(data) {
			t.Fatal("wire fields were dropped")
		}
	}
}
