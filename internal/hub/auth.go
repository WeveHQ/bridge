package hub

import (
	"errors"
	"net/http"
	"strings"

	"github.com/WeveHQ/bridge/internal/verifier"
)

func (server *Server) authenticateEdge(writer http.ResponseWriter, request *http.Request) (verifier.Claims, bool) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return verifier.Claims{}, false
	}

	header := request.Header.Get(authorizationHeader)
	if !strings.HasPrefix(header, "Bearer ") {
		http.Error(writer, "missing bearer token", http.StatusUnauthorized)
		return verifier.Claims{}, false
	}

	claims, err := server.tokenVerifier.Verify(request.Context(), strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		if errors.Is(err, verifier.ErrInvalidToken) {
			http.Error(writer, "invalid token", http.StatusUnauthorized)
			return verifier.Claims{}, false
		}

		server.logger.Warn("token verifier unavailable", "error", err)
		http.Error(writer, "token verifier unavailable", http.StatusServiceUnavailable)
		return verifier.Claims{}, false
	}
	if claims.BridgeID == "" {
		http.Error(writer, "invalid token", http.StatusUnauthorized)
		return verifier.Claims{}, false
	}

	return claims, true
}
