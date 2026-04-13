package hub

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/WeveHQ/bridge/internal/wire"
)

func decodeJSON(reader io.Reader, target any) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("missing body")
	}

	return json.Unmarshal(body, target)
}

func encodeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeReject(writer http.ResponseWriter, statusCode int, code string, message string) {
	encodeJSON(writer, statusCode, wire.DispatchReject{
		Error: wire.DispatchRejectError{
			Code:    code,
			Message: message,
		},
	})
}

func newReject(code string, message string) dispatchResult {
	return dispatchResult{
		reject: &wire.DispatchReject{
			Error: wire.DispatchRejectError{
				Code:    code,
				Message: message,
			},
		},
	}
}

func decodeBodySize(value string) (int, error) {
	if value == "" {
		return 0, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return 0, err
	}

	return len(decoded), nil
}
