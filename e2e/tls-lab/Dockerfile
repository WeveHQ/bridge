FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY e2e/tls-lab/main.go .
RUN CGO_ENABLED=0 go build -o /tls-target main.go
FROM alpine:3.24
COPY --from=build /tls-target /usr/local/bin/tls-target
ENTRYPOINT ["tls-target"]
