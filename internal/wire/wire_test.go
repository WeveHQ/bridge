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
