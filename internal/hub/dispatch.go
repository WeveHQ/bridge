package hub

import (
	"context"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

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

	if len(server.registry.inFlight) >= server.globalInFlight {
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

	server.registry.inFlight[newDispatchKey(bridgeID, dispatch.outboundTraceID)] = dispatch
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
		if server.cancelPendingDispatch(dispatch.bridgeID, dispatch.outboundTraceID) {
			server.logger.Warn("bridge did not pick up dispatch",
				"bridgeId", bridgeID,
				"outboundTraceId", dispatch.outboundTraceID,
				"grace", parkGrace.String(),
			)
			return newReject(wire.BridgeOfflineCode, "bridge did not pick up dispatch")
		}
	case <-ctx.Done():
		server.cancelPendingDispatch(dispatch.bridgeID, dispatch.outboundTraceID)
		return newReject(wire.BridgeResponseRejected, ctx.Err().Error())
	}

	select {
	case result := <-dispatch.result:
		return result
	case <-ctx.Done():
		server.removeInFlight(dispatch.bridgeID, dispatch.outboundTraceID)
		return newReject(wire.BridgeResponseRejected, ctx.Err().Error())
	}
}

func (server *Server) cancelPendingDispatch(bridgeID string, outboundTraceID string) bool {
	server.mu.Lock()
	key := newDispatchKey(bridgeID, outboundTraceID)
	dispatch, ok := server.registry.inFlight[key]
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
		delete(server.registry.inFlight, key)
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

func (server *Server) completeWithReject(bridgeID string, outboundTraceID string, code string, message string) bool {
	server.mu.Lock()
	key := newDispatchKey(bridgeID, outboundTraceID)
	dispatch, ok := server.registry.inFlight[key]
	if !ok {
		server.mu.Unlock()
		return false
	}

	delete(server.registry.inFlight, key)
	server.registry.completed[key] = server.now()
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

func (server *Server) removeInFlight(bridgeID string, outboundTraceID string) {
	server.mu.Lock()
	delete(server.registry.inFlight, newDispatchKey(bridgeID, outboundTraceID))
	recovered, remaining := server.clearGlobalRateLimitLocked()
	server.mu.Unlock()

	if recovered {
		server.logger.Info("hub in-flight pressure recovered",
			"limit", server.globalInFlight,
			"inFlight", remaining,
		)
	}
}
