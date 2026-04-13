package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"
	"time"

	"github.com/WeveHQ/bridge/internal/config"
	"github.com/WeveHQ/bridge/internal/edge"
)

func runEdge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("edge", flag.ContinueOnError)
	token := fs.String("token", "", "bridge token")
	hubURL := fs.String("hub-url", "", "hub base url")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.ParseEdgeConfig(config.EdgeInputs{
		Token:  *token,
		HubURL: *hubURL,
	})
	if err != nil {
		return err
	}

	runContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := edge.NewRunner(edge.Config{
		Token:             cfg.Token,
		HubURL:            cfg.HubURL,
		PollConcurrency:   cfg.PollConcurrency,
		HeartbeatInterval: time.Duration(cfg.HeartbeatSeconds) * time.Second,
		PollTimeout:       time.Duration(cfg.PollTimeoutMS) * time.Millisecond,
		AllowedHosts:      cfg.AllowedHosts,
	})

	return runner.Run(runContext)
}
