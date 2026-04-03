package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/WeveHQ/weve-bridge/internal/config"
)

func runHub(ctx context.Context, args []string) error {
	_ = ctx

	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	listenAddr := fs.String("listen", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.ParseHubConfig(config.HubInputs{
		ListenAddr: firstNonEmpty(*listenAddr, ""),
	})
	if err != nil {
		return err
	}

	return fmt.Errorf("hub not implemented yet on %s", cfg.ListenAddr)
}
