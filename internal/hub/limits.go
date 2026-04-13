package hub

func (server *Server) tryAcquirePollSlot(bridgeID string) bool {
	if server.perEdgeMaxPollConcurrency <= 0 {
		return true
	}

	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	current := server.registry.pollsByBridge[bridgeID]
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

	server.registry.pollsByBridge[bridgeID] = current + 1
	server.mu.Unlock()
	return true
}

func (server *Server) releasePollSlot(bridgeID string) {
	if server.perEdgeMaxPollConcurrency <= 0 {
		return
	}

	server.mu.Lock()
	state := server.getBridgeState(bridgeID)
	count := server.registry.pollsByBridge[bridgeID]
	recovered := state.pollRateLimited && count >= server.perEdgeMaxPollConcurrency
	if count <= 1 {
		delete(server.registry.pollsByBridge, bridgeID)
	} else {
		server.registry.pollsByBridge[bridgeID] = count - 1
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

func (server *Server) clearGlobalRateLimitLocked() (bool, int) {
	if !server.globalRateLimited {
		return false, len(server.registry.inFlight)
	}
	if len(server.registry.inFlight) >= server.globalInFlight {
		return false, len(server.registry.inFlight)
	}
	server.globalRateLimited = false
	return true, len(server.registry.inFlight)
}
