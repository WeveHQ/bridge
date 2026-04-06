package wire

import "encoding/json"

const (
	MaxBodyBytes           = 8 * 1024 * 1024
	V1PathPrefix           = "/v1"
	PollPath               = V1PathPrefix + "/poll"
	HeartbeatPath          = V1PathPrefix + "/heartbeat"
	ResponsePathPrefix     = V1PathPrefix + "/response/"
	DispatchPathPrefix     = "/v1/dispatch/"
	BridgeOfflineCode      = "bridge_offline"
	BridgeRateLimitedCode  = "bridge_rate_limited"
	BridgeRequestTooLarge  = "bridge_request_too_large"
	BridgeResponseTooLarge = "bridge_response_too_large"
	BridgeResponseRejected = "bridge_response_rejected"
)

type PollRequest struct {
	BridgeVersion string `json:"bridgeVersion"`
}

type PollResponse struct {
	OutboundTraceID string      `json:"outboundTraceId"`
	Req             HttpRequest `json:"req"`
}

type DispatchRequest struct {
	OutboundTraceID string      `json:"outboundTraceId"`
	Req             HttpRequest `json:"req"`
}

type DispatchReject struct {
	Error DispatchRejectError `json:"error"`
}

type DispatchRejectError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type HttpRequest struct {
	Method         string        `json:"method"`
	URL            string        `json:"url"`
	Headers        []HeaderEntry `json:"headers"`
	DeadlineUnixMs uint64        `json:"deadlineUnixMs"`
	Body           string        `json:"body"`
}

type HttpResponse struct {
	OutboundTraceID string        `json:"outboundTraceId"`
	Status          uint32        `json:"status"`
	Headers         []HeaderEntry `json:"headers"`
	Meta            ExecutionMeta `json:"meta"`
	Body            string        `json:"body"`
}

type HeaderEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ErrorKind string

const (
	ErrorKindNone              ErrorKind = "none"
	ErrorKindTimeout           ErrorKind = "timeout"
	ErrorKindDNS               ErrorKind = "dns"
	ErrorKindConnectionRefused ErrorKind = "connection_refused"
	ErrorKindTLS               ErrorKind = "tls"
	ErrorKindCanceled          ErrorKind = "canceled"
	ErrorKindConnectionReset   ErrorKind = "connection_reset"
	ErrorKindUnknown           ErrorKind = "unknown"
)

type ExecutionError struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
}

type ExecutionMeta struct {
	StartedAtUnixMs uint64          `json:"startedAtUnixMs"`
	DurationMs      uint32          `json:"durationMs"`
	BytesOut        uint64          `json:"bytesOut"`
	BytesIn         uint64          `json:"bytesIn"`
	Error           *ExecutionError `json:"error,omitempty"`
}

type HeartbeatRequest struct {
	BridgeVersion string `json:"bridgeVersion"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	UptimeSec     uint64 `json:"uptimeSec"`
	InFlight      uint32 `json:"inFlight"`
	Hostname      string `json:"hostname"`
}

type HeartbeatResponse struct {
	LatestVersion  string `json:"latestVersion"`
	MinimumVersion string `json:"minimumVersion"`
}

func MustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	return data
}
