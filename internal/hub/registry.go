package hub

import "time"

type dispatchKey struct {
	bridgeID        string
	outboundTraceID string
}

type registry struct {
	bridges       map[string]*bridgeState
	inFlight      map[dispatchKey]*dispatchState
	completed     map[dispatchKey]time.Time
	pollsByBridge map[string]int
}

func newRegistry() *registry {
	return &registry{
		bridges:       map[string]*bridgeState{},
		inFlight:      map[dispatchKey]*dispatchState{},
		completed:     map[dispatchKey]time.Time{},
		pollsByBridge: map[string]int{},
	}
}

func newDispatchKey(bridgeID string, outboundTraceID string) dispatchKey {
	return dispatchKey{
		bridgeID:        bridgeID,
		outboundTraceID: outboundTraceID,
	}
}

func (registry *registry) bridgeState(bridgeID string) *bridgeState {
	state, ok := registry.bridges[bridgeID]
	if ok {
		return state
	}

	state = &bridgeState{}
	registry.bridges[bridgeID] = state
	return state
}

func (registry *registry) countInFlightForBridge(bridgeID string) int {
	count := 0
	for _, dispatch := range registry.inFlight {
		if dispatch.bridgeID == bridgeID {
			count++
		}
	}
	return count
}

func (registry *registry) cleanupCompleted(now time.Time) {
	for key, completedAt := range registry.completed {
		if now.Sub(completedAt) > completedTTL {
			delete(registry.completed, key)
		}
	}
}
