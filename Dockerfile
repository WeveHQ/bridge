FROM golang:1.27.0-alpine AS build

WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o /out/weve-bridge ./cmd/bridge

FROM alpine:3.24

RUN apk add --no-cache wget && adduser -D -u 10001 bridge
USER bridge

COPY --from=build /out/weve-bridge /usr/local/bin/weve-bridge

ENTRYPOINT ["weve-bridge"]
