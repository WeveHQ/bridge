package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/verifier"
	"github.com/WeveHQ/bridge/internal/wire"
)

const (
	internalSecretHeader = "X-Internal-Secret"
	authorizationHeader  = "Authorization"
	heartbeatTTL         = 30 * time.Second
	parkGrace            = 2 * time.Second
	completedTTL         = 2 * time.Minute
)

type Config struct {
	TokenVerifier  verifier.TokenVerifier
	InternalSecret string
	PollHold       time.Duration
	GlobalInFlight int
	Now            func() time.Time
}

type Server struct {
	now            func() time.Time
	tokenVerifier  verifier.TokenVerifier
	internalSecret string
	pollHold       time.Duration
	globalInFlight int

	mu        sync.Mutex
	bridges   map[string]*bridgeState
	inFlight  map[string]*dispatchState
	completed map[string]time.Time
	draining  bool
}

type bridgeState struct {
	waiters       []chan *dispatchState
	pending       []*dispatchState
	lastHeartbeat time.Time
}

type dispatchState struct {
	bridgeID        string
	outboundTraceID string
	request         wire.HttpRequest
	pickedUp        chan struct{}
	result          chan dispatchResult
}

type dispatchResult struct {
	response *wire.HttpResponse
	reject   *wire.DispatchReject
}

func NewServer(cfg Config) *Server {
	if cfg.TokenVerifier == nil {
		panic("missing token verifier")
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Server{
		now:            now,
		tokenVerifier:  cfg.TokenVerifier,
		internalSecret: cfg.InternalSecret,
		pollHold:       cfg.PollHold,
		globalInFlight: cfg.GlobalInFlight,
		bridges:        map[string]*bridgeState{},
		inFlight:       map[string]*dispatchState{},
		completed:      map[string]time.Time{},
	}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wire.PollPath, server.handlePoll)
	mux.HandleFunc(wire.HeartbeatPath, server.handleHeartbeat)
	mux.HandleFunc(wire.ResponsePathPrefix, server.handleResponse)
	mux.HandleFunc(wire.DispatchPathPrefix, server.handleDispatch)
	return mux
}

func (server *Server) StartDrain() {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.draining = true
}

func (server *Server) handlePoll(writer http.ResponseWriter, request *http.Request) {
	claims, ok := server.authenticateEdge(writer, request)
	if !ok {
		return
	}

	var pollRequest wire.PollRequest
	if err := decodeJSON(request.Body, &pollRequest); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	dispatch, ok := server.acquireDispatch(request.Context(), claims.BridgeID)
	if !ok {
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
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	server.refreshHeartbeat(claims.BridgeID)
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
		server.completeWithReject(outboundTraceID, wire.BridgeResponseRejected, err.Error())
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if response.OutboundTraceID == "" {
		response.OutboundTraceID = outboundTraceID
	}
	if response.OutboundTraceID != outboundTraceID {
		server.completeWithReject(outboundTraceID, wire.BridgeResponseRejected, "response outbound trace id mismatch")
		http.Error(writer, "response outbound trace id mismatch", http.StatusBadRequest)
		return
	}

	if claims.BridgeID == "" {
		http.Error(writer, "missing bridge id", http.StatusUnauthorized)
		return
	}

	size, err := decodeBodySize(response.Body)
	if err != nil {
		server.completeWithReject(outboundTraceID, wire.BridgeResponseRejected, err.Error())
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if size > wire.MaxBodyBytes {
		server.completeWithReject(outboundTraceID, wire.BridgeResponseTooLarge, "response body exceeds max size")
		http.Error(writer, "response body exceeds max size", http.StatusRequestEntityTooLarge)
		return
	}

	server.mu.Lock()
	if dispatch, ok := server.inFlight[outboundTraceID]; ok {
		delete(server.inFlight, outboundTraceID)
		server.completed[outboundTraceID] = server.now()
		server.mu.Unlock()
		dispatch.result <- dispatchResult{response: &response}
		writer.WriteHeader(http.StatusOK)
		return
	}
	_, alreadyCompleted := server.completed[outboundTraceID]
	server.mu.Unlock()

	if alreadyCompleted {
		writer.WriteHeader(http.StatusOK)
		return
	}

	http.Error(writer, "unknown outbound trace id", http.StatusNotFound)
}

func (server *Server) handleDispatch(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(internalSecretHeader) != server.internalSecret {
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
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	size, err := decodeBodySize(dispatchRequest.Req.Body)
	if err != nil {
		writeReject(writer, http.StatusBadRequest, wire.BridgeResponseRejected, err.Error())
		return
	}
	if size > wire.MaxBodyBytes {
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

	writeReject(writer, statusCode, code, message)
}

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

		http.Error(writer, "token verifier unavailable", http.StatusServiceUnavailable)
		return verifier.Claims{}, false
	}
	if claims.BridgeID == "" {
		http.Error(writer, "invalid token", http.StatusUnauthorized)
		return verifier.Claims{}, false
	}

	return claims, true
}

func (server *Server) refreshHeartbeat(bridgeID string) {
	server.mu.Lock()
	defer server.mu.Unlock()

	state := server.getBridgeState(bridgeID)
	state.lastHeartbeat = server.now()
	server.cleanupCompleted()
}

func (server *Server) acquireDispatch(ctx context.Context, bridgeID string) (*dispatchState, bool) {
	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	state.lastHeartbeat = server.now()
	if len(state.pending) > 0 {
		dispatch := state.pending[0]
		state.pending = state.pending[1:]
		close(dispatch.pickedUp)
		server.mu.Unlock()
		return dispatch, true
	}

	waiter := make(chan *dispatchState, 1)
	state.waiters = append(state.waiters, waiter)
	server.mu.Unlock()

	timer := time.NewTimer(server.pollHold)
	defer timer.Stop()

	select {
	case dispatch := <-waiter:
		return dispatch, true
	case <-timer.C:
		server.removeWaiter(bridgeID, waiter)
		return nil, false
	case <-ctx.Done():
		server.removeWaiter(bridgeID, waiter)
		return nil, false
	}
}

func (server *Server) removeWaiter(bridgeID string, waiter chan *dispatchState) {
	server.mu.Lock()
	defer server.mu.Unlock()

	state := server.getBridgeState(bridgeID)
	for index, candidate := range state.waiters {
		if candidate != waiter {
			continue
		}

		state.waiters = append(state.waiters[:index], state.waiters[index+1:]...)
		return
	}
}

func (server *Server) dispatch(ctx context.Context, bridgeID string, request wire.DispatchRequest) dispatchResult {
	dispatch := &dispatchState{
		bridgeID:        bridgeID,
		outboundTraceID: request.OutboundTraceID,
		request:         request.Req,
		pickedUp:        make(chan struct{}),
		result:          make(chan dispatchResult, 1),
	}

	server.mu.Lock()
	if server.draining {
		server.mu.Unlock()
		return newReject(wire.BridgeOfflineCode, "hub is draining")
	}
	if len(server.inFlight) >= server.globalInFlight {
		server.mu.Unlock()
		return newReject(wire.BridgeRateLimitedCode, "hub in-flight limit reached")
	}
	if server.wasOfflineLocked(bridgeID) {
		server.mu.Unlock()
		return newReject(wire.BridgeOfflineCode, "bridge is offline")
	}

	state := server.getBridgeState(bridgeID)
	server.inFlight[dispatch.outboundTraceID] = dispatch
	if len(state.waiters) > 0 {
		waiter := state.waiters[0]
		state.waiters = state.waiters[1:]
		close(dispatch.pickedUp)
		waiter <- dispatch
	} else {
		state.pending = append(state.pending, dispatch)
	}
	server.cleanupCompleted()
	server.mu.Unlock()

	select {
	case <-dispatch.pickedUp:
	case <-time.After(parkGrace):
		if server.cancelPendingDispatch(dispatch.outboundTraceID) {
			return newReject(wire.BridgeOfflineCode, "bridge did not pick up dispatch")
		}
	case <-ctx.Done():
		server.cancelPendingDispatch(dispatch.outboundTraceID)
		return newReject(wire.BridgeResponseRejected, ctx.Err().Error())
	}

	select {
	case result := <-dispatch.result:
		return result
	case <-ctx.Done():
		server.removeInFlight(dispatch.outboundTraceID)
		return newReject(wire.BridgeResponseRejected, ctx.Err().Error())
	}
}

func (server *Server) cancelPendingDispatch(outboundTraceID string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()

	dispatch, ok := server.inFlight[outboundTraceID]
	if !ok {
		return false
	}

	state := server.getBridgeState(dispatch.bridgeID)
	for index, pending := range state.pending {
		if pending.outboundTraceID != outboundTraceID {
			continue
		}

		state.pending = append(state.pending[:index], state.pending[index+1:]...)
		delete(server.inFlight, outboundTraceID)
		return true
	}

	return false
}

func (server *Server) completeWithReject(outboundTraceID string, code string, message string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()

	dispatch, ok := server.inFlight[outboundTraceID]
	if !ok {
		return false
	}

	delete(server.inFlight, outboundTraceID)
	server.completed[outboundTraceID] = server.now()
	dispatch.result <- dispatchResult{
		reject: &wire.DispatchReject{
			Error: wire.DispatchRejectError{
				Code:    code,
				Message: message,
			},
		},
	}
	return true
}

func (server *Server) removeInFlight(outboundTraceID string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	delete(server.inFlight, outboundTraceID)
}

func (server *Server) wasOfflineLocked(bridgeID string) bool {
	state := server.getBridgeState(bridgeID)
	if len(state.waiters) > 0 {
		return false
	}
	if len(state.pending) > 0 {
		return false
	}

	return server.now().Sub(state.lastHeartbeat) > heartbeatTTL
}

func (server *Server) getBridgeState(bridgeID string) *bridgeState {
	state, ok := server.bridges[bridgeID]
	if ok {
		return state
	}

	state = &bridgeState{}
	server.bridges[bridgeID] = state
	return state
}

func (server *Server) cleanupCompleted() {
	now := server.now()
	for key, completedAt := range server.completed {
		if now.Sub(completedAt) > completedTTL {
			delete(server.completed, key)
		}
	}
}

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
