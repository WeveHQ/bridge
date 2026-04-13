package wire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPollResponseRoundTrip(t *testing.T) {
	t.Parallel()

	value := PollResponse{
		OutboundTraceID: "ot_123",
		Req: HttpRequest{
			Method:         "POST",
			URL:            "https://example.internal/search?q=alice",
			DeadlineUnixMs: 12345,
			Headers: []HeaderEntry{
				{Name: "content-type", Value: "application/json"},
				{Name: "x-test", Value: "abc"},
			},
			Body: "eyJvayI6dHJ1ZX0=",
		},
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var roundTrip PollResponse
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !reflect.DeepEqual(value, roundTrip) {
		t.Fatalf("round-trip mismatch: %#v != %#v", value, roundTrip)
	}
}

func TestBridgeStatusResponseRoundTrip(t *testing.T) {
	t.Parallel()

	lastHeartbeatAtUnixMs := uint64(12345)
	value := BridgeStatusResponse{
		BridgeID:              "brg_123",
		Alive:                 true,
		LastHeartbeatAtUnixMs: &lastHeartbeatAtUnixMs,
		WaiterCount:           2,
		PendingDispatchCount:  1,
		InFlightDispatchCount: 3,
		BridgeVersion:         "v1.2.3",
		Hostname:              "edge-host",
		OS:                    "linux",
		Arch:                  "amd64",
		UptimeSec:             600,
		EdgeInFlight:          4,
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var roundTrip BridgeStatusResponse
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !reflect.DeepEqual(value, roundTrip) {
		t.Fatalf("round-trip mismatch: %#v != %#v", value, roundTrip)
	}
}
