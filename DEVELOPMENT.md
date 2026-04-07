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
