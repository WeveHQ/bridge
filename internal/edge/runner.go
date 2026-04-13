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
	PollConcurrency   int
	HeartbeatInterval time.Duration
	PollTimeout       time.Duration
	AllowedHosts      []string
	Client            *http.Client
	Logger            *slog.Logger
}

type Runner struct {
	client            *http.Client
	token             string
	hubURL            string
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
		token:             cfg.Token,
		hubURL:            strings.TrimRight(cfg.HubURL, "/"),
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
		"pollConcurrency", runner.pollConcurrency,
		"heartbeatInterval", runner.heartbeatInterval.String(),
		"pollTimeout", runner.pollTimeout.String(),
		"allowedHostsCount", len(runner.allowedHosts),
		"allowListEnabled", len(runner.allowedHosts) > 0,
	)
	defer runner.logger.Info("edge stopped")

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, runner.pollConcurrency+1)
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
