FROM golang:1.26.1-alpine AS build

WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o /out/weve-bridge ./cmd/bridge

FROM alpine:3.22

RUN adduser -D -u 10001 bridge
USER bridge

COPY --from=build /out/weve-bridge /usr/local/bin/weve-bridge

ENTRYPOINT ["weve-bridge"]
