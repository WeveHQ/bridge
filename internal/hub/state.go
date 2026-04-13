package hub

import (
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

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

type bridgeTransition struct {
	bridgeID string
	alive    bool
	payload  *wire.HeartbeatRequest
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
	return server.registry.countInFlightForBridge(bridgeID)
}

func (server *Server) getBridgeState(bridgeID string) *bridgeState {
	return server.registry.bridgeState(bridgeID)
}

func (server *Server) cleanupCompleted() {
	server.registry.cleanupCompleted(server.now())
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
