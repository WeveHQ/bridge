# TLS protocol reference

For developers integrating with the Bridge hub. Tenant operators should start
with the [TLS operator guide](tls.md); the managed hub API is not an enrollment
or tenant administration interface.

## Compatibility and deployment

These fields describe the current source tree and require a release containing
TLS preflight and pinning support, plus a caller that sends the matching policy.
Upgrade the hub and edge before sending new operations or policy-bearing
requests. Older components can ignore unknown fields; there is no capability
negotiation. Disable dependent operations before rolling either component back.
Never retry a failed pinned dispatch after removing its policy.

## Wire contract

All operations use the existing authenticated `POST /v1/dispatch/{bridgeId}`
endpoint and existing edge poll/response flow. `outboundTraceId` remains required
for correlation. Omitted `operation` or `operation: "http"` means ordinary HTTP.
Unknown operations fail. The hub returns HTTP 400 with
`error.code: "invalid_request"` for invalid operation/policy payloads.

### Preflight

```json
{
  "outboundTraceId": "tls-check-123",
  "operation": "tls_preflight",
  "preflight": {
    "hostname": "splunk.internal",
    "port": 8089,
    "deadlineUnixMs": 1893456000000
  }
}
```

Replace the example deadline with a future Unix timestamp in milliseconds.
The host must be explicitly listed in `WEVE_BRIDGE_EDGE_ALLOWED_HOSTS`.
The probe uses the earlier of the supplied deadline and ten seconds. It resolves
DNS and opens TCP/TLS directly, bypassing `HTTPS_PROXY`; it does not establish
that a proxied HTTP route works. No HTTP request or TLS client certificate is
sent. It uses the same target trust roots as ordinary dispatch.

Do not include `req` with HTTP data. Unknown fields in a preflight payload are
rejected, including headers, bodies, and credentials. The hub emits a legacy `req` object with empty fields when forwarding this
envelope, matching the existing Go wire structure; consumers should discriminate on `operation`.

The response retains the usual `outboundTraceId`, `status`, `headers`, `body`,
and `meta` envelope, adding `preflight`:

```json
{
  "route": "direct",
  "dns": { "status": "passed" },
  "tcp": { "status": "passed" },
  "tls": { "status": "passed" },
  "hostname": { "status": "failed", "code": "hostname_mismatch" },
  "chain": { "status": "failed", "code": "unknown_authority" },
  "validity": { "status": "passed" },
  "verification": { "status": "failed", "code": "hostname_mismatch" },
  "legacyCnFallbackApplied": false,
  "authenticated": false,
  "certificate": {
    "subject": "CN=SplunkServerDefaultCert",
    "issuer": "CN=SplunkCommonCA",
    "dnsNames": [],
    "ipAddresses": [],
    "sanPresent": false,
    "notBefore": "2026-01-01T00:00:00Z",
    "notAfter": "2027-01-01T00:00:00Z",
    "certificateSha256": "64 lowercase hexadecimal characters"
  }
}
```

Each check has `status: "passed" | "failed" | "not_checked"` and an optional
stable `code`. Hostname checks use SANs; `verification` reports the existing
effective default verification, including the exact-match CN fallback.
Chain verification includes validity and server-auth usage, so a failed chain
check does not necessarily mean its CA is untrusted. Multiple checks can fail.
Opaque platform verification failures use `certificate_invalid`.

A completed diagnostic operation has no `meta.error`, even when certificate
verification fails. Network, deadline, invalid-input, and allowlist failures
set `meta.error` and preserve completed stage results. Preflight has `status: 0`
and an empty body because it never receives an HTTP response. `bytesOut` and
`bytesIn` describe HTTP bodies, not TLS records. Certificate metadata is omitted
with `diagnostics_too_large` if the diagnostic JSON would exceed 64 KiB.

`authenticated: false` means the metadata is an unverified observation. Never
promote its fingerprint into a trusted pin automatically. Confirm the expected
fingerprint through a trusted channel with the tenant.

Preflight is optional inspection for setup and troubleshooting. Its results are
informational and must not gate authenticated health checks. For a health check,
send the HTTP request with the configured TLS policy and use the actual dispatch's
HTTP response or structured execution error to determine the outcome. The edge
enforces the policy on that connection before transmitting HTTP credentials.
Callers should not duplicate certificate-policy interpretation using inspection
results. No prior preflight or stored inspection result is required.

### Pinned HTTP dispatch

```json
{
  "outboundTraceId": "request-123",
  "req": {
    "method": "GET",
    "url": "https://splunk.internal:8089/services/server/info?output_mode=json",
    "headers": [{ "name": "Authorization", "value": "Bearer <connector-token>" }],
    "body": "",
    "deadlineUnixMs": 1893456000000,
    "tlsPolicy": {
      "mode": "pinned_certificate",
      "origin": "https://splunk.internal:8089",
      "certificateSha256": "64 lowercase hexadecimal characters"
    }
  }
}
```

The fingerprint is SHA-256 of the entire leaf certificate DER, not its SPKI,
PEM text, or issuer certificate. The policy origin has no path, query, userinfo,
or fragment. Hostnames compare case-insensitively and the default port is 443.
Both the initial request and policy must name the same HTTPS origin. Pinning
requires an explicitly allowlisted hostname.

The pin replaces CA-chain trust and hostname matching. Before transmitting HTTP
data, Bridge checks the exact pin, leaf validity, server-auth usage, and
unsupported critical extensions. An absent EKU is supported for legacy targets.
Every pinned dispatch uses a dedicated transport without connection reuse or TLS
session resumption. Redirects are rejected before another request is sent.
Rotated certificates require an explicitly approved new pin.

Omitting `tlsPolicy` preserves existing target verification, including the narrow
allowlisted CN-only fallback. Pinning never changes edge-to-hub TLS. No insecure
mode, SPKI pinning, multiple-pin rotation, or new per-connector CA mode is added.

## Stable error codes

Execution errors keep `meta.error.kind` and `message` and add optional `code`.
Consumers should use codes, not parse Go error messages.

| Code | Meaning |
| --- | --- |
| `invalid_request` | Unknown operation/mode, malformed policy, or invalid target |
| `host_not_allowed` | Explicit host permission is absent |
| `dns` | Name resolution failed |
| `timeout`, `canceled` | Deadline or cancellation |
| `connection_refused`, `connection_reset` | TCP connection failure |
| `tls_handshake_failed` | TLS negotiation failed |
| `hostname_mismatch` | Certificate SANs do not match the requested identity |
| `unknown_authority` | Certificate chain could not reach a trusted root |
| `certificate_expired`, `certificate_not_yet_valid` | Certificate validity failure |
| `certificate_wrong_usage` | Certificate does not permit required server usage |
| `certificate_invalid` | Other certificate verification failure |
| `certificate_missing` | Peer did not supply a certificate |
| `certificate_pin_mismatch` | Leaf certificate differs from the approved pin |
| `redirect_rejected` | Pinned dispatch attempted a redirect |
| `diagnostics_too_large` | Certificate metadata exceeds the diagnostic limit |
| `execution_failed` | Other execution failure |

## Qualification lab

See [the simulated tenant qualification](tls-qualification.md) for recorded
results, an automated Docker test, and a persistent demonstration environment.
