# TLS guide for Bridge operators

This guide is for engineers installing the edge and connecting internal HTTPS
APIs. Weve operates the hub and supplies connector requests; you control the
edge's network access, hostname allowlist, and local CA trust.

**Availability:** TLS preflight and certificate pinning are new features in this
source tree. Confirm a released edge version and corresponding Weve support
before using them. The installed release's documentation and release notes take
precedence; this guide does not imply a particular dashboard control is available.

## Choose the appropriate trust configuration

| Target certificate | Configuration |
| --- | --- |
| Trusted issuer and a SAN matching the API hostname | Normal HTTPS verification; no pin needed |
| Private CA and a SAN matching the API hostname | Make the CA available to the edge |
| Trusted issuer, no SAN, and a CN exactly matching the API DNS hostname | Existing legacy compatibility for explicitly allowlisted hosts |
| Generic or mismatching identity that cannot be replaced immediately | Explicitly approved certificate pin, where supported by Weve |
| Expired or not yet valid | Correct the certificate or clock; pinning does not bypass validity checks |

Use a hostname-valid certificate with a trusted issuer when you can. Adding a CA
does not fix a hostname mismatch. Adding a hostname to the allowlist does not
make its certificate trusted.

## Private certificate authorities

Bridge uses the runtime's trust configuration for ordinary HTTPS verification.
For a private CA, provide a PEM CA bundle using `SSL_CERT_FILE`. The path must
exist **inside the edge container or host**, and the edge user must be able to
read it. The official image runs as UID 10001.

For example, using the image, enrollment token, and hub URL from the
[installation guide](../README.md#docker):

```bash
docker run -d --name weve-bridge \
  --restart unless-stopped \
  -e WEVE_BRIDGE_EDGE_TOKEN \
  -e WEVE_BRIDGE_EDGE_HUB_URL \
  -e WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=api.corp.example \
  -e SSL_CERT_FILE=/etc/weve/ca-bundle.pem \
  --mount type=bind,src=/absolute/path/ca-bundle.pem,dst=/etc/weve/ca-bundle.pem,readonly \
  "$WEVE_BRIDGE_IMAGE" edge
```

Replace the host path and hostname. This is an alternative to the initial
`docker run` command, not a second container with the same name.

The bundle contains trusted CA certificates, not private keys. It affects
edge-to-hub HTTPS and ordinary target HTTPS; preserve the roots needed for both.
On Linux, `SSL_CERT_FILE` changes the CA bundle file used by Go, while certificate
directories may also contribute roots. Restart the edge after changing the
bundle so subsequent connections use the new trust configuration.

Configure the target to serve any required intermediate certificates. A pin is
not a substitute for correcting an incomplete chain when normal verification
is suitable.

## Existing CN-only compatibility

For an explicitly allowlisted DNS hostname, Bridge accepts a certificate without
a SAN only if its Common Name exactly matches that hostname, ignoring case.
The chain must be trusted, the certificate must be within its validity period,
and its usage must permit TLS server authentication.

Wildcard and IP-address Common Names are not eligible. A certificate with any
SAN extension never uses this fallback, even when its SAN does not match.
This behavior is automatic for eligible target hosts. It does not apply to the
edge-to-hub connection.

## Optional certificate inspection

Where supported, Weve can ask the edge to run a TLS preflight for an explicitly
allowlisted hostname and port during setup or troubleshooting. This inspection
is optional; it is not a prerequisite for a connector health check. You do not
need to expose an inbound diagnostic port or give tenant users access to the
hub API.

The edge checks DNS, TCP connectivity, and TLS, then returns:

- Certificate hostname verification, chain verification, and validity results.
- Subject, issuer, SANs, validity dates, and the leaf certificate fingerprint.
- Whether the existing CN-only compatibility rule permits the connection.

The preflight sends no HTTP request, bearer token, cookie, request body, or TLS
client certificate to the target. It can inspect public certificate information
even when verification fails. That metadata returns to Weve and may contain
internal hostnames or organization names; it is not private key material.

A completed TLS handshake alone does not establish trust. Multiple checks may
fail at once. Chain verification also checks validity and usage, so its failure
does not always mean the CA is unknown. For a supported CN-only certificate,
SAN hostname verification can fail while the effective verification succeeds.

Preflight connects **directly** from the edge, with a maximum duration of ten
seconds. It bypasses `HTTPS_PROXY`, so it does not prove that a proxied HTTP route
works. Health checks send the actual API request with the configured TLS policy
and report its response or execution error. The edge verifies that connection
before sending credentials, whether or not inspection has been run. Inspection
results do not authorize a later request or override its verification outcome.

## Approve a legacy certificate pin

Pinning is an explicit exception for an endpoint whose certificate cannot pass
normal CA and hostname verification. It trusts **one exact leaf certificate**
for one HTTPS hostname and port.

1. Confirm the intended API hostname and port, and add the hostname to the edge's
   allowlist.
2. Obtain the leaf certificate or its SHA-256 fingerprint through a trusted
   administrative channel, such as the service's certificate inventory or an
   administrator with access to the server configuration.
3. Confirm the certificate is current and belongs to the intended service.
4. Configure the approved fingerprint and HTTPS origin through the supported
   Weve connector configuration or with Weve support. There is no edge
   environment variable or CLI flag for pins.
5. Run the connector health check to verify the real authenticated request.

If you have the server's leaf certificate in PEM format, inspect its fingerprint
locally with OpenSSL:

```bash
openssl x509 -in server-certificate.pem -noout -fingerprint -sha256
```

This is the fingerprint of the complete certificate, not the issuing CA or the
public key alone. Bridge's policy format uses the 64 hexadecimal digits in
lowercase, without colon separators. See the
[OpenSSL certificate command reference](https://docs.openssl.org/3.0/man1/openssl-x509/).

Do not approve a fingerprint solely because an unverified network probe returned
it. A certificate and private key shared across vendor installations also cannot
uniquely identify your installation; use an installation-specific certificate
and key.

For a pinned request, Bridge replaces CA-chain trust and hostname matching with
the exact fingerprint check. It still enforces certificate validity, compatible
server-auth usage, and supported critical extensions. Connector credentials are
sent only after these checks pass. Pinned requests use fresh connections and do
not follow redirects; configure the final API address directly.

## Certificate renewal and rotation

Every leaf certificate replacement changes its fingerprint, including renewal
using the same private key. The old pin will reject the replacement.

Obtain and approve the new certificate fingerprint through the same trusted
process, coordinate its activation with the Weve configuration change, and run
the connector health check afterward. There is one active pin per request;
Bridge does not provide an overlapping multi-pin rotation window. Plan for a
possible interruption during the change. Do not remove the policy just to get
past a pin mismatch.

## Proxies and TLS interception

HTTP-client traffic honors `HTTPS_PROXY`, `HTTP_PROXY`, and `NO_PROXY`. Configure
`NO_PROXY` for internal targets that should be reached directly. Preflight is
always direct, regardless of those settings.

For an approved TLS-intercepting proxy, make its CA available to the edge for
ordinary verification. A pinned target must present the approved leaf
certificate: a proxy substituting another certificate will be rejected.
Pinning applies only to target requests and does not relax hub TLS verification.

## Troubleshooting

| Finding or code | Operator action |
| --- | --- |
| `host_not_allowed` | Confirm the destination is intended and add its exact hostname, without a scheme or port. |
| `dns` | Check name resolution inside the edge's network or container. |
| `connection_refused` | Verify the API port and that the service is listening. |
| `timeout` | Check routing, firewall rules, service responsiveness, and the supplied deadline. |
| `hostname_mismatch` | Use the hostname in the certificate SAN or replace the certificate with one for the intended API hostname. |
| `unknown_authority` | Check the target's intermediate chain and the edge's trusted CA bundle. |
| `certificate_expired` / `certificate_not_yet_valid` | Check certificate dates and the edge's system clock; renew or correct the certificate. |
| `certificate_wrong_usage` | Obtain a certificate suitable for TLS server authentication. |
| `certificate_pin_mismatch` | Investigate renewal, interception, or a wrong endpoint; independently verify any replacement pin. |
| `redirect_rejected` | Set the connector to its intended final HTTPS API endpoint and approve that origin's certificate. |
| `tls_handshake_failed` | Check that this port serves TLS and supports compatible protocols and authentication requirements. |
| `certificate_invalid` | Review the certificate with the service administrator; some platforms return a general verification failure. |

When contacting `engineering@weve.security`, include the edge version, timestamp,
outbound trace ID, relevant error code, and whether the route uses a proxy. Share
internal endpoint metadata through your approved support channel. Do not send
private keys, enrollment tokens, or connector credentials.

For an isolated evaluation, see the [TLS qualification lab](tls-qualification.md).
For API implementation details, see the [protocol reference](tls-protocol.md).
