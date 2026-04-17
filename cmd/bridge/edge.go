package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WeveHQ/bridge/internal/config"
	"github.com/WeveHQ/bridge/internal/edge"
	"github.com/WeveHQ/bridge/internal/logging"
)

func runEdge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("edge", flag.ContinueOnError)
	token := fs.String("token", "", "bridge token")
	hubURL := fs.String("hub-url", "", "hub base url")
	healthListenAddr := fs.String("health-listen", "", "health listener address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.ParseEdgeConfig(config.EdgeInputs{
		Token:            *token,
		HubURL:           *hubURL,
		HealthListenAddr: *healthListenAddr,
	})
	if err != nil {
		return err
	}

	logger, err := logging.New(os.Stdout, logging.Config{
		Level:  cfg.Log.Level,
		Format: cfg.Log.Format,
	})
	if err != nil {
		return err
	}
	logger = logger.With("component", "edge")

	runContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := edge.NewRunner(edge.Config{
		Token:             cfg.Token,
		HubURL:            cfg.HubURL,
		HealthListenAddr:  cfg.HealthListenAddr,
		PollConcurrency:   cfg.PollConcurrency,
		HeartbeatInterval: time.Duration(cfg.HeartbeatSeconds) * time.Second,
		PollTimeout:       time.Duration(cfg.PollTimeoutMS) * time.Millisecond,
		AllowedHosts:      cfg.AllowedHosts,
		Logger:            logger,
	})

	return runner.Run(runContext)
}
