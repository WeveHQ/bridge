package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/logging"
	"github.com/WeveHQ/bridge/internal/verifier"
	"github.com/WeveHQ/bridge/internal/wire"
)

const (
	bridgeHubSecretHeader = "X-Bridge-Hub-Secret"
	authorizationHeader   = "Authorization"
	heartbeatTTL          = 30 * time.Second
	parkGrace             = 2 * time.Second
	completedTTL          = 2 * time.Minute
)

type Config struct {
	TokenVerifier             verifier.TokenVerifier
	HubSecret                 string
	PollHold                  time.Duration
	GlobalInFlight            int
	PerEdgeMaxPollConcurrency int
	Now                       func() time.Time
	Logger                    *slog.Logger
}

type Server struct {
	now                       func() time.Time
	tokenVerifier             verifier.TokenVerifier
	hubSecret                 string
	pollHold                  time.Duration
	globalInFlight            int
	perEdgeMaxPollConcurrency int
	logger                    *slog.Logger

	mu                sync.Mutex
	bridges           map[string]*bridgeState
	inFlight          map[string]*dispatchState
	completed         map[string]time.Time
	pollsByBridge     map[string]int
	draining          bool
	globalRateLimited bool
}

type bridgeState struct {
	waiters              []chan *dispatchState
	pending              []*dispatchState
	lastHeartbeat        time.Time
	lastHeartbeatPayload *wire.HeartbeatRequest
	seen                 bool
	aliveKnown           bool
	alive                bool
	pollRateLimited      bool
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

type bridgeTransition struct {
	bridgeID string
	alive    bool
	payload  *wire.HeartbeatRequest
}

func NewServer(cfg Config) *Server {
	if cfg.TokenVerifier == nil {
		panic("missing token verifier")
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	logger := cfg.Logger
	if logger == nil {
		logger = logging.Discard()
	}

	return &Server{
		now:                       now,
		tokenVerifier:             cfg.TokenVerifier,
		hubSecret:                 cfg.HubSecret,
		pollHold:                  cfg.PollHold,
		globalInFlight:            cfg.GlobalInFlight,
		perEdgeMaxPollConcurrency: cfg.PerEdgeMaxPollConcurrency,
		logger:                    logger,
		bridges:                   map[string]*bridgeState{},
		inFlight:                  map[string]*dispatchState{},
		completed:                 map[string]time.Time{},
		pollsByBridge:             map[string]int{},
	}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wire.PollPath, server.handlePoll)
	mux.HandleFunc(wire.HeartbeatPath, server.handleHeartbeat)
	mux.HandleFunc(wire.ResponsePathPrefix, server.handleResponse)
	mux.HandleFunc(wire.DispatchPathPrefix, server.handleDispatch)
	mux.HandleFunc(wire.BridgeStatusPathPrefix, server.handleBridgeStatus)
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
		server.completeWithReject(outboundTraceID, wire.BridgeResponseRejected, err.Error())
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
		server.logger.Warn("response rejected due to invalid body encoding",
			"bridgeId", claims.BridgeID,
			"outboundTraceId", outboundTraceID,
			"error", err,
		)
		server.completeWithReject(outboundTraceID, wire.BridgeResponseRejected, err.Error())
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
		server.completeWithReject(outboundTraceID, wire.BridgeResponseTooLarge, "response body exceeds max size")
		http.Error(writer, "response body exceeds max size", http.StatusRequestEntityTooLarge)
		return
	}

	server.mu.Lock()
	if dispatch, ok := server.inFlight[outboundTraceID]; ok {
		delete(server.inFlight, outboundTraceID)
		server.completed[outboundTraceID] = server.now()
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

	_, alreadyCompleted := server.completed[outboundTraceID]
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

func (server *Server) refreshHeartbeat(bridgeID string, heartbeat wire.HeartbeatRequest) {
	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	firstSeen := !state.seen
	reconnected := state.seen && state.aliveKnown && !state.alive
	state.seen = true
	state.aliveKnown = true
	state.alive = true
	state.lastHeartbeat = server.now()
	state.lastHeartbeatPayload = &heartbeat
	server.cleanupCompleted()
	server.mu.Unlock()

	switch {
	case firstSeen:
		server.logger.Info("bridge connected",
			append(server.bridgeLogAttrs(bridgeID, &heartbeat), "event", "first_seen")...,
		)
	case reconnected:
		server.logger.Info("bridge reconnected",
			server.bridgeLogAttrs(bridgeID, &heartbeat)...,
		)
	}
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
		server.logger.Debug("dispatch handed to waiting poller",
			"bridgeId", bridgeID,
			"outboundTraceId", dispatch.outboundTraceID,
		)
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

func (server *Server) tryAcquirePollSlot(bridgeID string) bool {
	if server.perEdgeMaxPollConcurrency <= 0 {
		return true
	}

	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	current := server.pollsByBridge[bridgeID]
	if current >= server.perEdgeMaxPollConcurrency {
		shouldLog := !state.pollRateLimited
		state.pollRateLimited = true
		server.mu.Unlock()
		if shouldLog {
			server.logger.Warn("bridge poll concurrency limited",
				"bridgeId", bridgeID,
				"currentPolls", current,
				"limit", server.perEdgeMaxPollConcurrency,
			)
		}
		return false
	}

	server.pollsByBridge[bridgeID] = current + 1
	server.mu.Unlock()
	return true
}

func (server *Server) releasePollSlot(bridgeID string) {
	if server.perEdgeMaxPollConcurrency <= 0 {
		return
	}

	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	count := server.pollsByBridge[bridgeID]
	recovered := state.pollRateLimited && count >= server.perEdgeMaxPollConcurrency
	if count <= 1 {
		delete(server.pollsByBridge, bridgeID)
	} else {
		server.pollsByBridge[bridgeID] = count - 1
	}
	if recovered {
		state.pollRateLimited = false
	}
	server.mu.Unlock()

	if recovered {
		server.logger.Info("bridge poll concurrency recovered",
			"bridgeId", bridgeID,
			"limit", server.perEdgeMaxPollConcurrency,
		)
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
		shouldLog := !server.globalRateLimited
		server.globalRateLimited = true
		server.mu.Unlock()
		if shouldLog {
			server.logger.Warn("hub in-flight limit reached", "limit", server.globalInFlight)
		}
		return newReject(wire.BridgeRateLimitedCode, "hub in-flight limit reached")
	}

	state := server.getBridgeState(bridgeID)
	transition, alive := server.observeBridgeHealthLocked(bridgeID, state)
	if !alive {
		server.mu.Unlock()
		server.logBridgeTransition(transition)
		return newReject(wire.BridgeOfflineCode, "bridge is offline")
	}

	server.inFlight[dispatch.outboundTraceID] = dispatch
	server.cleanupCompleted()
	if len(state.waiters) > 0 {
		waiter := state.waiters[0]
		state.waiters = state.waiters[1:]
		close(dispatch.pickedUp)
		waiter <- dispatch
		server.mu.Unlock()
		server.logger.Debug("dispatch delivered directly to waiting poller",
			"bridgeId", bridgeID,
			"outboundTraceId", dispatch.outboundTraceID,
		)
	} else {
		state.pending = append(state.pending, dispatch)
		pendingCount := len(state.pending)
		server.mu.Unlock()
		server.logger.Debug("dispatch queued for bridge",
			"bridgeId", bridgeID,
			"outboundTraceId", dispatch.outboundTraceID,
			"pendingDispatches", pendingCount,
		)
	}

	select {
	case <-dispatch.pickedUp:
	case <-time.After(parkGrace):
		if server.cancelPendingDispatch(dispatch.outboundTraceID) {
			server.logger.Warn("bridge did not pick up dispatch",
				"bridgeId", bridgeID,
				"outboundTraceId", dispatch.outboundTraceID,
				"grace", parkGrace.String(),
			)
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
	dispatch, ok := server.inFlight[outboundTraceID]
	if !ok {
		server.mu.Unlock()
		return false
	}

	state := server.getBridgeState(dispatch.bridgeID)
	for index, pending := range state.pending {
		if pending.outboundTraceID != outboundTraceID {
			continue
		}

		state.pending = append(state.pending[:index], state.pending[index+1:]...)
		delete(server.inFlight, outboundTraceID)
		recovered, remaining := server.clearGlobalRateLimitLocked()
		server.mu.Unlock()
		if recovered {
			server.logger.Info("hub in-flight pressure recovered",
				"limit", server.globalInFlight,
				"inFlight", remaining,
			)
		}
		return true
	}

	server.mu.Unlock()
	return false
}

func (server *Server) completeWithReject(outboundTraceID string, code string, message string) bool {
	server.mu.Lock()
	dispatch, ok := server.inFlight[outboundTraceID]
	if !ok {
		server.mu.Unlock()
		return false
	}

	delete(server.inFlight, outboundTraceID)
	server.completed[outboundTraceID] = server.now()
	recovered, remaining := server.clearGlobalRateLimitLocked()
	server.mu.Unlock()

	dispatch.result <- dispatchResult{
		reject: &wire.DispatchReject{
			Error: wire.DispatchRejectError{
				Code:    code,
				Message: message,
			},
		},
	}

	if recovered {
		server.logger.Info("hub in-flight pressure recovered",
			"limit", server.globalInFlight,
			"inFlight", remaining,
		)
	}
	return true
}

func (server *Server) removeInFlight(outboundTraceID string) {
	server.mu.Lock()
	delete(server.inFlight, outboundTraceID)
	recovered, remaining := server.clearGlobalRateLimitLocked()
	server.mu.Unlock()

	if recovered {
		server.logger.Info("hub in-flight pressure recovered",
			"limit", server.globalInFlight,
			"inFlight", remaining,
		)
	}
}

func (server *Server) wasOfflineLocked(bridgeID string) bool {
	return !server.bridgeAliveLocked(server.getBridgeState(bridgeID))
}

func (server *Server) bridgeAliveLocked(state *bridgeState) bool {
	if len(state.waiters) > 0 {
		return true
	}
	if len(state.pending) > 0 {
		return true
	}
	return server.now().Sub(state.lastHeartbeat) <= heartbeatTTL
}

func (server *Server) getBridgeStatus(bridgeID string) wire.BridgeStatusResponse {
	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	transition, alive := server.observeBridgeHealthLocked(bridgeID, state)
	status := wire.BridgeStatusResponse{
		BridgeID:              bridgeID,
		Alive:                 alive,
		WaiterCount:           uint32(len(state.waiters)),
		PendingDispatchCount:  uint32(len(state.pending)),
		InFlightDispatchCount: uint32(server.countInFlightForBridgeLocked(bridgeID)),
	}

	if !state.lastHeartbeat.IsZero() {
		lastHeartbeatAtUnixMs := uint64(state.lastHeartbeat.UnixMilli())
		status.LastHeartbeatAtUnixMs = &lastHeartbeatAtUnixMs
	}

	if state.lastHeartbeatPayload != nil {
		status.BridgeVersion = state.lastHeartbeatPayload.BridgeVersion
		status.Hostname = state.lastHeartbeatPayload.Hostname
		status.OS = state.lastHeartbeatPayload.OS
		status.Arch = state.lastHeartbeatPayload.Arch
		status.UptimeSec = state.lastHeartbeatPayload.UptimeSec
		status.EdgeInFlight = state.lastHeartbeatPayload.InFlight
	}
	server.mu.Unlock()

	server.logBridgeTransition(transition)
	return status
}

func (server *Server) countInFlightForBridgeLocked(bridgeID string) int {
	count := 0
	for _, dispatch := range server.inFlight {
		if dispatch.bridgeID == bridgeID {
			count++
		}
	}
	return count
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

func (server *Server) observeBridgeHealthLocked(bridgeID string, state *bridgeState) (*bridgeTransition, bool) {
	alive := server.bridgeAliveLocked(state)
	if !state.seen {
		return nil, alive
	}
	if !state.aliveKnown {
		state.aliveKnown = true
		state.alive = alive
		return nil, alive
	}
	if state.alive == alive {
		return nil, alive
	}

	state.alive = alive
	return &bridgeTransition{
		bridgeID: bridgeID,
		alive:    alive,
		payload:  state.lastHeartbeatPayload,
	}, alive
}

func (server *Server) logBridgeTransition(transition *bridgeTransition) {
	if transition == nil {
		return
	}

	attrs := server.bridgeLogAttrs(transition.bridgeID, transition.payload)
	if transition.alive {
		server.logger.Info("bridge reconnected", attrs...)
		return
	}
	server.logger.Warn("bridge went offline", attrs...)
}

func (server *Server) bridgeLogAttrs(bridgeID string, heartbeat *wire.HeartbeatRequest) []any {
	attrs := []any{"bridgeId", bridgeID}
	if heartbeat == nil {
		return attrs
	}
	if heartbeat.Hostname != "" {
		attrs = append(attrs, "hostname", heartbeat.Hostname)
	}
	if heartbeat.BridgeVersion != "" {
		attrs = append(attrs, "bridgeVersion", heartbeat.BridgeVersion)
	}
	if heartbeat.OS != "" {
		attrs = append(attrs, "os", heartbeat.OS)
	}
	if heartbeat.Arch != "" {
		attrs = append(attrs, "arch", heartbeat.Arch)
	}
	attrs = append(attrs,
		"uptimeSec", heartbeat.UptimeSec,
		"edgeInFlight", heartbeat.InFlight,
	)
	return attrs
}

func (server *Server) clearGlobalRateLimitLocked() (bool, int) {
	if !server.globalRateLimited {
		return false, len(server.inFlight)
	}
	if len(server.inFlight) >= server.globalInFlight {
		return false, len(server.inFlight)
	}
	server.globalRateLimited = false
	return true, len(server.inFlight)
}

func (server *Server) logDispatchReject(bridgeID string, outboundTraceID string, code string, message string) {
	switch code {
	case wire.BridgeOfflineCode, wire.BridgeRateLimitedCode:
		server.logger.Debug("dispatch rejected",
			"bridgeId", bridgeID,
			"outboundTraceId", outboundTraceID,
			"code", code,
			"message", message,
		)
	default:
		server.logger.Warn("dispatch rejected",
			"bridgeId", bridgeID,
			"outboundTraceId", outboundTraceID,
			"code", code,
			"message", message,
		)
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
