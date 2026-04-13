package hub

import (
	"net/http"
	"strings"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/wire"
)

func (server *Server) handlePoll(writer http.ResponseWriter, request *http.Request) {
	claims, ok := server.authenticateEdge(writer, request)
	if !ok {
		return
	}

	var pollRequest wire.PollRequest
	if err := decodeJSON(request.Body, &pollRequest); err != nil {
		server.logger.Warn("poll request rejected",
			"bridgeId", claims.BridgeID,
			"error", err,
		)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if !server.tryAcquirePollSlot(claims.BridgeID) {
		writeReject(writer, http.StatusTooManyRequests, wire.BridgePollRateLimited, "per-edge poll concurrency limit reached")
		return
	}
	defer server.releasePollSlot(claims.BridgeID)

	dispatch, ok := server.acquireDispatch(request.Context(), claims.BridgeID)
	if !ok {
		server.logger.Debug("poll returned no work", "bridgeId", claims.BridgeID)
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	encodeJSON(writer, http.StatusOK, wire.PollResponse{
		OutboundTraceID: dispatch.outboundTraceID,
		Req:             dispatch.request,
	})
}

func (server *Server) handleHeartbeat(writer http.ResponseWriter, request *http.Request) {
	claims, ok := server.authenticateEdge(writer, request)
	if !ok {
		return
	}

	var heartbeat wire.HeartbeatRequest
	if err := decodeJSON(request.Body, &heartbeat); err != nil {
		server.logger.Warn("heartbeat rejected",
			"bridgeId", claims.BridgeID,
			"error", err,
		)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	server.refreshHeartbeat(claims.BridgeID, heartbeat)
	encodeJSON(writer, http.StatusOK, wire.HeartbeatResponse{
		LatestVersion:  build.Version,
		MinimumVersion: build.Version,
	})
}

func (server *Server) handleResponse(writer http.ResponseWriter, request *http.Request) {
	claims, ok := server.authenticateEdge(writer, request)
	if !ok {
		return
	}

	outboundTraceID := strings.TrimPrefix(request.URL.Path, wire.ResponsePathPrefix)
	if outboundTraceID == "" {
		http.Error(writer, "missing outbound trace id", http.StatusBadRequest)
		return
	}

	var response wire.HttpResponse
	if err := decodeJSON(request.Body, &response); err != nil {
		server.logger.Warn("response rejected",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
			"error", err,
		)
		server.completeWithReject(claims.BridgeID, outboundTraceID, wire.BridgeResponseRejected, err.Error())
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if response.OutboundTraceID == "" {
		response.OutboundTraceID = outboundTraceID
	}
	if response.OutboundTraceID != outboundTraceID {
		server.logger.Warn("response rejected due to outbound trace mismatch",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
			"responseOutboundTraceId", response.OutboundTraceID,
		)
		server.completeWithReject(claims.BridgeID, outboundTraceID, wire.BridgeResponseRejected, "response outbound trace id mismatch")
		http.Error(writer, "response outbound trace id mismatch", http.StatusBadRequest)
		return
	}

	if claims.BridgeID == "" {
		http.Error(writer, "missing bridge id", http.StatusUnauthorized)
		return
	}

	size, err := decodeBodySize(response.Body)
	if err != nil {
		server.logger.Warn("response rejected due to invalid body encoding",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
			"error", err,
		)
		server.completeWithReject(claims.BridgeID, outboundTraceID, wire.BridgeResponseRejected, err.Error())
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if size > wire.MaxBodyBytes {
		server.logger.Warn("response rejected because body is too large",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
			"bodySize", size,
			"maxBodyBytes", wire.MaxBodyBytes,
		)
		server.completeWithReject(claims.BridgeID, outboundTraceID, wire.BridgeResponseTooLarge, "response body exceeds max size")
		http.Error(writer, "response body exceeds max size", http.StatusRequestEntityTooLarge)
		return
	}

	server.mu.Lock()
	key := newDispatchKey(claims.BridgeID, outboundTraceID)
	if dispatch, ok := server.registry.inFlight[key]; ok {
		delete(server.registry.inFlight, key)
		server.registry.completed[key] = server.now()
		recovered, remaining := server.clearGlobalRateLimitLocked()
		server.mu.Unlock()
		dispatch.result <- dispatchResult{response: &response}
		if recovered {
			server.logger.Info("hub in-flight pressure recovered",
				"limit", server.globalInFlight,
				"inFlight", remaining,
			)
		}
		writer.WriteHeader(http.StatusOK)
		return
	}

	_, alreadyCompleted := server.registry.completed[key]
	server.mu.Unlock()

	if alreadyCompleted {
		server.logger.Debug("duplicate response acknowledged",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
		)
		writer.WriteHeader(http.StatusOK)
		return
	}

	server.logger.Warn("response rejected for unknown outbound trace id",
		"bridgeId", claims.BridgeID,
		"outboundTraceId", outboundTraceID,
	)
	http.Error(writer, "unknown outbound trace id", http.StatusNotFound)
}

func (server *Server) handleDispatch(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(bridgeHubSecretHeader) != server.hubSecret {
		server.logger.Warn("dispatch rejected with invalid hub secret")
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bridgeID := strings.TrimPrefix(request.URL.Path, wire.DispatchPathPrefix)
	if bridgeID == "" {
		http.Error(writer, "missing bridge id", http.StatusBadRequest)
		return
	}

	var dispatchRequest wire.DispatchRequest
	if err := decodeJSON(request.Body, &dispatchRequest); err != nil {
		server.logger.Warn("dispatch rejected",
			"bridgeId", bridgeID,
			"error", err,
		)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	size, err := decodeBodySize(dispatchRequest.Req.Body)
	if err != nil {
		server.logger.Warn("dispatch rejected due to invalid body encoding",
			"bridgeId", bridgeID,
			"outboundTraceId", dispatchRequest.OutboundTraceID,
			"error", err,
		)
		writeReject(writer, http.StatusBadRequest, wire.BridgeResponseRejected, err.Error())
		return
	}
	if size > wire.MaxBodyBytes {
		server.logger.Warn("dispatch rejected because body is too large",
			"bridgeId", bridgeID,
			"outboundTraceId", dispatchRequest.OutboundTraceID,
			"bodySize", size,
			"maxBodyBytes", wire.MaxBodyBytes,
		)
		writeReject(writer, http.StatusRequestEntityTooLarge, wire.BridgeRequestTooLarge, "request body exceeds max size")
		return
	}

	result := server.dispatch(request.Context(), bridgeID, dispatchRequest)
	if result.response != nil {
		encodeJSON(writer, http.StatusOK, result.response)
		return
	}

	statusCode := http.StatusBadGateway
	code := wire.BridgeResponseRejected
	message := "dispatch failed"
	if result.reject != nil {
		code = result.reject.Error.Code
		message = result.reject.Error.Message
		switch code {
		case wire.BridgeOfflineCode:
			statusCode = http.StatusServiceUnavailable
		case wire.BridgeRateLimitedCode:
			statusCode = http.StatusTooManyRequests
		case wire.BridgeRequestTooLarge, wire.BridgeResponseTooLarge:
			statusCode = http.StatusRequestEntityTooLarge
		}
	}

	server.logDispatchReject(bridgeID, dispatchRequest.OutboundTraceID, code, message)
	writeReject(writer, statusCode, code, message)
}

func (server *Server) handleBridgeStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(bridgeHubSecretHeader) != server.hubSecret {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(request.URL.Path, wire.BridgeStatusPathPrefix)
	if path == request.URL.Path || !strings.HasSuffix(path, wire.BridgeStatusPathSuffix) {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}

	bridgeID := strings.TrimSuffix(path, wire.BridgeStatusPathSuffix)
	bridgeID = strings.TrimSuffix(bridgeID, "/")
	if bridgeID == "" {
		http.Error(writer, "missing bridge id", http.StatusBadRequest)
		return
	}

	encodeJSON(writer, http.StatusOK, server.getBridgeStatus(bridgeID))
}
