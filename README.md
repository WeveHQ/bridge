# Weve Bridge

Connect HTTP APIs inside your network to Weve through an outbound connection.

<div>

[![Signed](https://img.shields.io/badge/signed-cosign-green)](https://github.com/WeveHQ/bridge/releases)
[![SLSA](https://img.shields.io/badge/SLSA-Level%203-green)](https://slsa.dev)
[![SBOM](https://img.shields.io/badge/SBOM-CycloneDX-blue)](https://github.com/WeveHQ/bridge/releases)
[![Release](https://img.shields.io/github/v/release/WeveHQ/bridge?sort=semver)](https://github.com/WeveHQ/bridge/releases)
[![CI](https://github.com/WeveHQ/bridge/actions/workflows/checks.yml/badge.svg)](https://github.com/WeveHQ/bridge/actions/workflows/checks.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/WeveHQ/bridge)](go.mod)
[![Image](https://img.shields.io/badge/container-ghcr.io-blue)](https://github.com/WeveHQ/bridge/pkgs/container/weve-bridge)
[![License](https://img.shields.io/github/license/WeveHQ/bridge)](LICENSE)

</div>

## How it works

You run the **edge** in your network. Weve operates the **hub**. The edge opens
outbound HTTPS connections to the hub, receives requests, calls your internal
APIs, and returns their responses. You do not need an inbound connection from
Weve or a published container port for this traffic.

Bridge forwards HTTP requests and responses; it does not provide a VPN or a
SOCKS proxy. Set an explicit hostname allowlist to restrict the destinations
Weve can request. **If the allowlist is unset, ordinary HTTP dispatch can reach
any host accessible to the edge.** Use your network controls to restrict ports
and destination IPs as needed.

## Documentation

| Task | Guide |
| --- | --- |
| Install and operate the edge | This README |
| Configure certificate trust, diagnose TLS, or approve a legacy certificate | [TLS operator guide](docs/tls.md) |
| Verify a downloaded binary | [Release verification](docs/releases.md) |
| Evaluate TLS behavior in an isolated environment | [TLS qualification lab](docs/tls-qualification.md) |
| Build, test, or contribute | [Development](DEVELOPMENT.md) · [Contributing](CONTRIBUTING.md) |
| Integrate with the hub API | [TLS protocol reference](docs/tls-protocol.md) |

Documentation describes this source tree. For an installed release, use the
README and documentation at its release tag. TLS preflight and certificate
pinning are new source-tree features: confirm a released edge version and
corresponding Weve support before relying on them. They are not enabled by an
edge upgrade alone.

## Before you install

You need:

- A Linux host or container runtime with access to the internal APIs you intend
  to connect. Linux images support `amd64` and `arm64`.
- An enrollment token and the HTTPS hub URL supplied by Weve for your Bridge.
- Outbound access to that hub on TCP 443, directly or through your approved proxy.
- DNS resolution and network access from the edge to each target's API hostname
  and port. A product's web-console port may differ from its API port.
- A trusted certificate chain and matching hostname for each HTTPS target. See
  [TLS configuration](docs/tls.md) for private CAs and legacy certificates.

1 vCPU and 256 MB RAM are a starting allocation; size and monitor the deployment
for your request volume and payloads.

The enrollment token authenticates the edge to Weve. It is separate from the
credentials Weve uses for your target API. Store it using your deployment's
secret-management mechanism.

## Install

### Docker

Create a Bridge in Weve and obtain its token and hub URL. Export
`WEVE_BRIDGE_EDGE_TOKEN` and `WEVE_BRIDGE_EDGE_HUB_URL` in your environment, then
select an image from [Releases](https://github.com/WeveHQ/bridge/releases).
Replace `<version>` and the example hostname below with your chosen values.
Use a versioned image or approved digest for a repeatable deployment.

```bash
export WEVE_BRIDGE_IMAGE='ghcr.io/wevehq/weve-bridge:<version>'

docker run -d --name weve-bridge \
  --restart unless-stopped \
  -e WEVE_BRIDGE_EDGE_TOKEN \
  -e WEVE_BRIDGE_EDGE_HUB_URL \
  -e WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=api.corp.example \
  "$WEVE_BRIDGE_IMAGE" edge
```

The command runs **edge mode**. You do not need to install a hub in your tenant
network. No host port is published by this example.

Check startup and connection logs:

```bash
docker logs --tail 100 weve-bridge
```

A `connected to hub` message means a heartbeat was acknowledged. Verify the
Bridge's connection status in Weve, then run the connector's health check to
confirm access to the actual target.

### Binary

Download the archive for your operating system and architecture from
[Releases](https://github.com/WeveHQ/bridge/releases) and follow
[release verification](docs/releases.md) before extracting it.

With the enrollment token, hub URL, and allowlist set in the environment:

```bash
./weve-bridge edge
```

For a persistent installation, run the binary under your host's service manager
and configure it to restart after a failure or reboot. For source builds, see
[Development](DEVELOPMENT.md#building).

## Configuration

The environment variables below configure edge mode. The CLI also accepts
`--token`, `--hub-url`, and `--health-listen`; supplied values override their
environment equivalents.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `WEVE_BRIDGE_EDGE_TOKEN` | Yes | — | Enrollment token |
| `WEVE_BRIDGE_EDGE_HUB_URL` | Yes | — | HTTPS hub URL supplied by Weve |
| `WEVE_BRIDGE_EDGE_ALLOWED_HOSTS` | Recommended | Unrestricted ordinary dispatch | Comma-separated target hostnames, without schemes, ports, or paths |
| `WEVE_BRIDGE_EDGE_HEALTH_LISTEN_ADDR` | No | `0.0.0.0:8080` | Local health endpoint listener |
| `WEVE_BRIDGE_EDGE_POLL_CONCURRENCY` | No | `4` | Number of waiting polls maintained with the hub; not a hard limit on executing requests |
| `WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS` | No | `15` | Heartbeat interval |
| `WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS` | No | `30000` | Long-poll timeout in milliseconds |
| `WEVE_BRIDGE_LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, or `error` |
| `WEVE_BRIDGE_LOG_FORMAT` | No | `json` | `json` or `text` |
| `HTTPS_PROXY` | No | — | Proxy for HTTPS HTTP-client traffic |
| `HTTP_PROXY` | No | — | Proxy for plain HTTP target traffic |
| `NO_PROXY` | No | — | Hosts and addresses that bypass the proxy |
| `SSL_CERT_FILE` | No | System trust configuration | PEM CA bundle path inside the edge runtime; see [TLS configuration](docs/tls.md#private-certificate-authorities) |

### Hostname allowlist

```bash
WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=api.corp.example,search.corp.example
```

Matching is case-insensitive and exact by hostname. Entries do not support
wildcards, URL paths, or port restrictions. List every intended hostname,
including redirect destinations for ordinary HTTP requests. An allowed hostname
is permission to connect; it does not establish certificate trust.

TLS preflight, explicit certificate pinning, and the legacy CN fallback require
an explicit allowlist entry. Pinned requests do not follow redirects.

### Health checks

`GET /healthz` returns HTTP 200 and `OK`. **This is a process liveness check.**
It does not test hub connectivity, token validity, target access, or certificates.
Use Weve connection status and connector health checks for those checks.

The image includes `wget`. To probe from inside the running container:

```bash
docker exec weve-bridge wget -qO- http://127.0.0.1:8080/healthz
```

For Kubernetes, this liveness probe assumes the default listener:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
```

The health listener binds to all interfaces by default. Keep it reachable only
by your local monitoring infrastructure; no public health port is required.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Missing token or hub URL at startup | Ensure the required environment variables reach the container or service. |
| `heartbeat failed` or `poll failed` with `invalid token` | Verify the enrollment token and intended hub URL with Weve. |
| `token verifier unavailable` | Contact Weve support; hub-side token verification is unavailable. |
| TLS error during heartbeat or polling | Check the hub hostname, outbound proxy, and CA trust. This is the edge-to-hub connection, not the target API. |
| `407 Proxy Authentication Required` | Check the configured proxy's authentication requirements. |
| `poll rate limited by hub` | The hub is limiting waiting polls. The edge backs off automatically; review poll concurrency with Weve. |
| `host_not_allowed` | Confirm that the destination is intended, then add its exact hostname to the allowlist. |
| Dispatch error `dns`, `connection_refused`, or `timeout` | Check target DNS, API port, routing, and firewall rules from the edge's network. |
| Target certificate or pin error | Follow the [TLS troubleshooting guide](docs/tls.md#troubleshooting). |

The edge writes connection and dispatch logs to stdout. Dispatch records include
an outbound trace ID, target hostname, timing, and execution outcome. Capture the
trace ID, edge version, timestamp, and relevant error when requesting support.
Review logs for sensitive information before sharing them publicly; do not
include enrollment tokens, target credentials, or private keys.

## Upgrades

Read the release notes, select the new image version or binary, and restart the
edge with your existing configuration. Verify the hub connection and connector
health afterward. Coordinate features that require hub support with Weve. Before
rolling back an edge, check whether any connector depends on a feature the older
version cannot enforce, such as certificate pinning.

## Support and security

- Service status: [status.weve.security](https://status.weve.security)
- Operational support: `engineering@weve.security`
- Vulnerability reporting: [Security policy](SECURITY.md)
- Source license: [Apache 2.0](LICENSE)
