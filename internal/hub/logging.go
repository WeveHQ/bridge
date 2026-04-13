package hub

import "github.com/WeveHQ/bridge/internal/wire"

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
