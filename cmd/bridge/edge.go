package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/WeveHQ/weve-bridge/internal/config"
)

func runEdge(ctx context.Context, args []string) error {
	_ = ctx

	fs := flag.NewFlagSet("edge", flag.ContinueOnError)
	token := fs.String("token", "", "bridge token")
	hubURL := fs.String("hub-url", "", "hub base url")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.ParseEdgeConfig(config.EdgeInputs{
		Token:  firstNonEmpty(*token, ""),
		HubURL: firstNonEmpty(*hubURL, ""),
	})
	if err != nil {
		return err
	}

	return fmt.Errorf("edge not implemented yet for bridge %s via %s", cfg.BridgeID, cfg.HubURL)
}
