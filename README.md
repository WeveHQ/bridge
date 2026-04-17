# weve/bridge

**Egress-only, zero-dependency agent to plug private data sources into Weve.**

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

---

## What?

Weve Bridge connects the Weve SaaS Cloud to data sources inside your network.

Run a single Go container on your network. It dials out to Weve over HTTPS/443 and waits. When Weve needs to query one of your private targets, the request travels back down that outbound connection. No inbound firewall rules.

Very slim, zero dependencies, ~10 MB.

## How?

1. Edge dials out to Weve on 443 and parks long-polls.
2. When Weve dispatches a request, the hub hands it down an already-open poll.
3. Edge executes the HTTP call against your internal target.
4. Response travels back up the same path.

Scoped to HTTP request/responses only. This NOT a VPN and NOT a SOCKS tunnel. Weve can only reach targets that are on your allow-list. All HTTP requests performed by this are logged for audit (both in stdout and in the Weve platform).

## Requirements

- 1 vCPU / 256 MB recommended.
- Linux container runtime (Docker, Kubernetes, ECS, Nomad) or a Linux host
- Outbound HTTPS/443 to `*.weve.security`
- An enrollment token from the Weve dashboard
- Optional: corporate proxy via `HTTPS_PROXY`, custom CA via `SSL_CERT_FILE`

## Enroll

1. In the Weve dashboard, open **Settings → Connectors → Private Network Access → New bridge**.
2. Copy the enrollment token.
3. Set `WEVE_BRIDGE_EDGE_TOKEN`, `WEVE_BRIDGE_EDGE_HUB_URL` and start the container.
4. The dashboard shows the bridge as `connected` within 60 seconds.

Tokens are scoped to your tenant and a single bridge. Treat them as secrets.

## Install

### Docker

```bash
docker run -d --name weve-bridge \
  -e WEVE_BRIDGE_EDGE_TOKEN=$WEVE_BRIDGE_EDGE_TOKEN \
  -e WEVE_BRIDGE_EDGE_HUB_URL=$WEVE_BRIDGE_EDGE_HUB_URL \
  -e WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=splunk.corp.internal,okta.corp.internal \
  ghcr.io/wevehq/weve-bridge:latest edge
```

> This same binary can act in _Hub Mode_ or _Edge Mode_. You want to run Edge Mode on your network. Weve runs Hub Mode in the cloud.

### Binary

Download from [Releases](https://github.com/WeveHQ/bridge/releases).

> Optionally, you can verify the cosign signature and SLSA attestation before running.

```bash
cosign verify-blob \
  --certificate weve-bridge_linux_amd64.pem \
  --signature weve-bridge_linux_amd64.sig \
  weve-bridge_linux_amd64

./weve-bridge edge
```

### Compiling from source

Requires Go 1.26+. Clone and build the `bridge` command:

```bash
git clone https://github.com/WeveHQ/bridge.git
cd bridge
go build -o weve-bridge ./cmd/bridge

./weve-bridge edge
```

Cross-compile for Linux from any host by setting `GOOS=linux GOARCH=amd64` (or `arm64`).

## Configure

All configuration is through environment variables.

| Variable                              | Required | Default        | Purpose                                           |
| ------------------------------------- | -------- | -------------- | ------------------------------------------------- |
| `WEVE_BRIDGE_EDGE_TOKEN`              | yes      | —              | Enrollment token from the Weve dashboard          |
| `WEVE_BRIDGE_EDGE_HUB_URL`            | yes      | —              | Bridge endpoint for your tenant (from dashboard)  |
| `WEVE_BRIDGE_EDGE_HEALTH_LISTEN_ADDR` | no       | `0.0.0.0:8080` | Address the edge health endpoint listens on       |
| `WEVE_BRIDGE_EDGE_ALLOWED_HOSTS`      | no       | —              | Comma-separated internal host allow-list          |
| `WEVE_BRIDGE_EDGE_POLL_CONCURRENCY`   | no       | `4`            | Concurrent in-flight requests this edge handles   |
| `WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS`  | no       | `15`           | Heartbeat interval to the hub                     |
| `WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS`    | no       | `30000`        | Long-poll timeout in milliseconds                 |
| `WEVE_BRIDGE_LOG_LEVEL`               | no       | `info`         | `debug` / `info` / `warn` / `error`               |
| `WEVE_BRIDGE_LOG_FORMAT`              | no       | `json`         | `json` / `text`                                   |
| `HTTPS_PROXY`                         | no       | —              | Corporate egress proxy                            |
| `NO_PROXY`                            | no       | —              | Proxy bypass list                                 |
| `SSL_CERT_FILE`                       | no       | —              | Custom CA bundle for TLS-intercepting middleboxes |

### Health checks

Weve Bridge exposes `GET /healthz` returning HTTP `200` with body `OK` (text).

- Port can be tweaked with `WEVE_BRIDGE_EDGE_HEALTH_LISTEN_ADDR` (default `0.0.0.0:8080`).
- The container image includes `wget`, so you can use either HTTP probes or in-container `exec` probes.

e.g. Docker Compose:

```yaml
services:
  edge:
    image: ghcr.io/wevehq/weve-bridge:latest
    command: ['edge']
    healthcheck:
      test: ['CMD', 'wget', '-qO-', 'http://127.0.0.1:8080/healthz']
      interval: 30s
      timeout: 5s
      retries: 3
```

e.g. Kubernetes HTTP probe:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
```

e.g. Kubernetes exec probe:

```yaml
livenessProbe:
  exec:
    command:
      - wget
      - -qO-
      - http://127.0.0.1:8080/healthz
```

### Allow-list

`WEVE_BRIDGE_EDGE_ALLOWED_HOSTS` optional, but recommended (defense in depth). Any request whose target host is not on this list is rejected by the edge before it hits the network. Weve Cloud cannot bypass it. Leave it unset to allow all.

```bash
WEVE_BRIDGE_EDGE_ALLOWED_HOSTS=splunk.corp.internal,okta.corp.internal,jira.corp.internal
```

### Corporate proxy

`HTTPS_PROXY` and `NO_PROXY` are honored via the Go stdlib. No extra configuration needed.

### TLS interception

Point `SSL_CERT_FILE` at your CA bundle. The edge does not pin certificates — you can MITM it.

## Troubleshooting

| Symptom                                                                                                                      | What it usually means / what to check                                                                                                                                                              |
| ---------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `heartbeat failed` or `poll failed` with `invalid token`                                                                     | The enrollment token was rejected by the hub/token verifier. Check that the token is current/correct.                                                                                              |
| `heartbeat failed` or `poll failed` with `token verifier unavailable`                                                        | Hub-side token verification is failing or unreachable. This is usually a issue in the Weve side. Reach out to `engineering@weve.security`.                                                         |
| `heartbeat failed` or `poll failed` with a TLS / certificate / x509 error                                                    | The edge could not establish TLS to the hub, proxy, or target. If you use TLS interception, set `SSL_CERT_FILE` to your CA bundle.                                                                 |
| `407 Proxy Authentication Required`                                                                                          | The configured `HTTPS_PROXY` requires credentials the container/host does not have.                                                                                                                |
| `poll rate limited by hub`                                                                                                   | The hub is enforcing its per-edge poll concurrency limit. The edge backs off automatically; Benign, but you should lower `WEVE_BRIDGE_EDGE_POLL_CONCURRENCY`.                                      |
| `dispatch completed with execution error` and `host not allowed: <host>`                                                     | The target hostname is not in `WEVE_BRIDGE_EDGE_ALLOWED_HOSTS`. Matching is exact by hostname (no wildcards). Could be on purpose; if not, either add the host or remove the env var to allow all. |
| `dispatch completed with execution error` and `errorKind=dns`, `timeout`, `connection_refused`, `tls`, or `connection_reset` | The edge received the dispatch but failed to reach the internal target. Check internal DNS, connectivity, deadlines, and the target's TLS chain.                                                   |

Run with `WEVE_BRIDGE_LOG_LEVEL=debug` for verbose diagnostics.

## Support

- Status: [https://status.weve.security](https://status.weve.security)
- Support: `engineering@weve.security`

## License

[Apache 2.0](LICENSE)
