# Simulated tenant TLS qualification

This optional lab is for operators evaluating TLS behavior and contributors
testing changes. It builds Bridge hub and edge images from the local source tree.
It provides a disposable internal TLS service with generated certificates and
server-side HTTP counters. All tokens, keys, and certificates are test data.

## Topology

```mermaid
flowchart LR
    Test[Qualification runner / simulated Weve] --> Hub
    Edge -->|Outbound poll and response| Hub
    Edge -->|Private target requests| Target[Tenant TLS target]
    Test -->|Local management port: inventory and counters| Target
```

The hub and verifier share a control network. The edge also joins an internal
Docker tenant network with the target. The hub cannot resolve/reach the target
directly; the qualification suite checks this. A separate management network
allows the host test runner to read certificate inventory and counters through a
port bound to `127.0.0.1`. Target TLS ports are not published to the host.

The target generates two independent CAs. Only the tenant CA is mounted into the
edge through `SSL_CERT_FILE`. Generic legacy certificates use the other CA.

| Target port | Certificate / behavior |
| --- | --- |
| 8443 | Trusted CA, matching DNS SAN |
| 8444 | Trusted CA, matching CN, no SAN |
| 8445 | Untrusted private CA, generic `SplunkServerDefaultCert` CN, no SAN |
| 8446 | Expired certificate |
| 8447 | Not-yet-valid certificate |
| 8448 | Certificate permitting client authentication only |
| 8449 | Authenticated endpoint that redirects to a cleartext sink |
| 8080 | Cleartext sink counting any redirected request |
| 8082 | Lab management API: health, certificate inventory, counters, rotation |

## Automated qualification

From the repository root, with Docker running and the Go version required by
[go.mod](../go.mod) installed:

```bash
go test -v -count=1 -tags docker ./e2e -run '^TestTenantTLSQualification$'
```

The test creates a uniquely named Compose project, allocates loopback ports,
builds Linux/amd64 images, runs each scenario, and removes the containers,
networks, and certificate volume afterward. On failure, container logs are included in the
test output. It is also picked up by the existing Docker E2E CI command.

The assertions inspect both Bridge's responses and independent server counters:

- Every preflight sends zero target HTTP requests.
- Strict SAN and exact-match CN compatibility both work with the mounted CA.
- A generic untrusted certificate fails ordinary dispatch and works with its
  independently approved leaf fingerprint.
- Wrong pins, expired/future certificates, and wrong usage send zero target HTTP
  requests and deliver no bearer token to an HTTP handler.
- A pinned redirect sends one authenticated request to the approved endpoint,
  then rejects the redirect. The cleartext sink receives zero requests.
- After certificate rotation, the old pin sends zero HTTP requests. Approving
  the replacement fingerprint from the management inventory restores access.
- Missing DNS records, a closed port, and a disallowed host return distinct codes.

The management inventory supplies the approved fingerprint independently of the
preflight response. This models confirmation by the tenant engineer and avoids
automatic trust-on-first-use.

## Keep the lab running for a demonstration

```bash
docker compose -f e2e/tls-lab/compose.yml -p bridge-tls-demo up -d --build
```

Defaults: hub `http://127.0.0.1:18080`, management
`http://127.0.0.1:18082`. Override `TLS_LAB_HUB_PORT` and `TLS_LAB_ADMIN_PORT` if
those ports are occupied. Allow several seconds for the edge to connect.

Inspect server-owned fingerprints and HTTP counters:

```bash
curl -s http://127.0.0.1:18082/state
```

Run a credential-free preflight using the existing hub API:

```bash
deadline_ms=$(( ($(date +%s) + 30) * 1000 ))
curl -s http://127.0.0.1:18080/v1/dispatch/bridge_123 \
  -H 'X-Bridge-Hub-Secret: internal-secret' \
  -H 'Content-Type: application/json' \
  --data @- <<JSON
{"outboundTraceId":"demo-preflight-${deadline_ms}","operation":"tls_preflight","preflight":{"hostname":"tls-target","port":8445,"deadlineUnixMs":${deadline_ms}}}
JSON
```

This sets a request deadline thirty seconds ahead. Each preflight is capped at
ten seconds regardless.
The result should show reachable TLS, hostname mismatch, unknown authority, and
an unverified certificate. Reading `/state` again should show no HTTP requests.

Use [the TLS protocol reference](tls-protocol.md) to dispatch to
`https://tls-target:8445/services/server/info`, with the fixture credential
`Authorization: Bearer simulated-connector-token`. Obtain the approved pin from
`endpoints.generic.fingerprint` in `/state`. The policy origin is
`https://tls-target:8445`.

Rotate the certificate without restarting the service:

```bash
curl -s -X POST http://127.0.0.1:18082/rotate
```

Repeat the pinned request with the old fingerprint: it must fail with
`certificate_pin_mismatch`, without increasing the HTTP counter. Use a unique
`outboundTraceId` for each dispatch. Then approve the replacement fingerprint
from `/state` and repeat to demonstrate recovery.

Remove the disposable environment:

```bash
docker compose -f e2e/tls-lab/compose.yml -p bridge-tls-demo down -v --remove-orphans
```

## Limits of this qualification

The suite exercises the Bridge protocol and TLS enforcement against real containers,
DNS, TCP connections, and generated certificates. The runner substitutes for
Weve; it does not exercise connector persistence, user approval, UI diagnostics,
or the real Splunk API. The simulated control plane uses HTTP on its Docker
network. Production hub HTTPS, tenant proxies, firewalls, actual certificate
chains still need validation in their intended deployment. On an ARM development
machine, Linux/amd64 containers run under Docker emulation; this is a functional
qualification, not a production performance benchmark. Set `TLS_LAB_PLATFORM`
to `linux/arm64` to exercise that architecture separately.

The disposable management API has no authentication and must remain local to
this lab. Never expose it publicly or use real tenant credentials here.
