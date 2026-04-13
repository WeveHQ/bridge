package hub

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

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
