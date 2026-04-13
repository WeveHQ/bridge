package testsupport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/WeveHQ/bridge/internal/verifier"
)

type StaticVerifier struct {
	ClaimsByToken map[string]verifier.Claims
	Err           error
}

func (stub StaticVerifier) Verify(_ context.Context, token string) (verifier.Claims, error) {
	if stub.Err != nil {
		return verifier.Claims{}, stub.Err
	}

	claims, ok := stub.ClaimsByToken[token]
	if !ok {
		return verifier.Claims{}, verifier.ErrInvalidToken
	}

	return claims, nil
}

func StartVerifierServer(t testing.TB, token string, secret string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "invalid token", http.StatusUnauthorized)
			return
		}
		if request.Header.Get(verifier.SecretHeader()) != secret {
			http.Error(writer, "invalid secret", http.StatusUnauthorized)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"tenantId":"tenant_123","bridgeId":"bridge_123"}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}
