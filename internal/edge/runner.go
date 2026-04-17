package edge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WeveHQ/bridge/internal/healthz"
	"github.com/WeveHQ/bridge/internal/logging"
)

const (
	retryInterval          = 10 * time.Second
	pollRateLimitedBackoff = 1 * time.Second
)

var errPollRateLimited = errors.New("poll rate limited by hub")

type Config struct {
	Token             string
	HubURL            string
	HealthListenAddr  string
	PollConcurrency   int
	HeartbeatInterval time.Duration
	PollTimeout       time.Duration
	AllowedHosts      []string
	Client            *http.Client
	Logger            *slog.Logger
}

type Runner struct {
	client            *http.Client
	executor          *executor
	token             string
	hubURL            string
	healthListenAddr  string
	pollConcurrency   int
	heartbeatInterval time.Duration
	pollTimeout       time.Duration
	allowedHosts      []string
	startedAt         time.Time
	inFlight          atomic.Int32
	logger            *slog.Logger

	stateMu          sync.Mutex
	connectedLogged  bool
	heartbeatFailing bool
	pollFailing      bool
}

func NewRunner(cfg Config) *Runner {
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = logging.Discard()
	}

	return &Runner{
		client:            client,
		executor:          newExecutor(cfg.Client, cfg.AllowedHosts),
		token:             cfg.Token,
		hubURL:            strings.TrimRight(cfg.HubURL, "/"),
		healthListenAddr:  cfg.HealthListenAddr,
		pollConcurrency:   cfg.PollConcurrency,
		heartbeatInterval: cfg.HeartbeatInterval,
		pollTimeout:       cfg.PollTimeout,
		allowedHosts:      cfg.AllowedHosts,
		startedAt:         time.Now(),
		logger:            logger,
	}
}

func (runner *Runner) Run(ctx context.Context) error {
	runner.logger.Info("edge starting",
		"hubURL", runner.hubURL,
		"healthListenAddr", runner.healthListenAddr,
		"pollConcurrency", runner.pollConcurrency,
		"heartbeatInterval", runner.heartbeatInterval.String(),
		"pollTimeout", runner.pollTimeout.String(),
		"allowedHostsCount", len(runner.allowedHosts),
		"allowListEnabled", len(runner.allowedHosts) > 0,
	)
	defer runner.logger.Info("edge stopped")

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, runner.pollConcurrency+2)
	if runner.healthListenAddr != "" {
		runner.startHealthServer(ctx, errCh)
	}
	go runner.runHeartbeatLoop(heartbeatCtx, errCh)

	for index := 0; index < runner.pollConcurrency; index++ {
		go runner.runPollSlot(ctx, errCh)
	}

	select {
	case <-ctx.Done():
		runner.logger.Info("edge shutting down", "reason", ctx.Err())
		return nil
	case err := <-errCh:
		if err == nil {
			return nil
		}
		runner.logger.Error("edge runner failed", "error", err)
		return err
	}
}

func (runner *Runner) startHealthServer(ctx context.Context, errCh chan<- error) {
	mux := http.NewServeMux()
	mux.HandleFunc(healthz.Path, healthz.Handler)

	server := &http.Server{
		Addr:    runner.healthListenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	go func() {
		runner.logger.Info("edge health listening", "listenAddr", runner.healthListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errCh <- err:
			case <-ctx.Done():
			}
		}
	}()
}
