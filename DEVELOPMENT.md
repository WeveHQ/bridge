# Development Guide

## Prerequisites

- **Go 1.26.1+** — [install](https://go.dev/dl/)
- **Docker** and **Docker Compose** — required for e2e tests and container builds

## Building

```bash
# Build the binary
go build -o weve-bridge ./cmd/bridge

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o weve-bridge ./cmd/bridge
GOOS=linux GOARCH=arm64 go build -o weve-bridge ./cmd/bridge
```

CGO is not required (`CGO_ENABLED=0`).

## Testing

### Unit tests

```bash
go test ./...

# With race detector
go test -race ./...

# Verbose, single package
go test -v ./internal/config
```

### Integration tests

Integration tests in `integration/` build and run the full binary, spinning up local hub and edge instances against test HTTP servers.

```bash
go test -v ./integration
```

### End-to-end tests

E2E tests in `e2e/` use Docker Compose to run the full stack (hub, edge, token verifier, target HTTP server).

```bash
go test -v ./e2e -tags docker
```

These require Docker and Docker Compose to be available.

## Docker

### Build the image locally

```bash
docker build -t weve-bridge .
```

### Run via Docker Compose (e2e stack)

```bash
cd e2e
docker compose up
```

This starts hub, edge, a mock token verifier, and a target HTTP server.

### Hub Environment Variables

> Weve runs the hub in the cloud. These variables are only relevant if you are operating your own hub (e.g. local dev).

| Variable                                        | Required | Default | Purpose                                                  |
| ----------------------------------------------- | -------- | ------- | -------------------------------------------------------- |
| `WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL`            | yes      | —       | URL the hub calls to verify edge enrollment tokens       |
| `WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET`         | yes      | —       | Shared secret presented to the token verifier            |
| `WEVE_BRIDGE_HUB_SECRET`                        | yes      | —       | Shared secret for internal hub-to-control-plane traffic  |
| `WEVE_BRIDGE_HUB_LISTEN_ADDR`                   | no       | `:8080` | Address the hub listens on                               |
| `WEVE_BRIDGE_HUB_VERIFY_TIMEOUT_MS`             | no       | `2000`  | Timeout for token verifier calls                         |
| `WEVE_BRIDGE_HUB_VERIFY_CACHE_SECONDS`          | no       | `30`    | How long to cache successful token verifications         |
| `WEVE_BRIDGE_HUB_POLL_HOLD_SECONDS`             | no       | `25`    | How long the hub holds a long-poll before returning idle |
| `WEVE_BRIDGE_HUB_GLOBAL_IN_FLIGHT`              | no       | `64`    | Max concurrent in-flight requests across all edges       |
| `WEVE_BRIDGE_HUB_PER_EDGE_MAX_POLL_CONCURRENCY` | no       | `0`     | Per-edge cap on poll concurrency (`0` = unlimited)       |
| `WEVE_BRIDGE_LOG_LEVEL`                         | no       | `info`  | `debug` / `info` / `warn` / `error`                      |
| `WEVE_BRIDGE_LOG_FORMAT`                        | no       | `json`  | `json` / `text`                                          |
