package main

import (
	"context"
	"flag"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/WeveHQ/weve-bridge/internal/config"
	"github.com/WeveHQ/weve-bridge/internal/hub"
	"github.com/WeveHQ/weve-bridge/internal/verifier"
)

func runHub(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	listenAddr := fs.String("listen", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.ParseHubConfig(config.HubInputs{
		ListenAddr: *listenAddr,
	})
	if err != nil {
		return err
	}

	tokenVerifier, err := verifier.NewClient(verifier.Config{
		URL:      cfg.VerifyTokenURL,
		CacheTTL: time.Duration(cfg.VerifyCacheSeconds) * time.Second,
		Client: &http.Client{
			Timeout: time.Duration(cfg.VerifyTimeoutMS) * time.Millisecond,
		},
	})
	if err != nil {
		return err
	}

	server := hub.NewServer(hub.Config{
		TokenVerifier:  tokenVerifier,
		InternalSecret: cfg.InternalSecret,
		PollHold:       time.Duration(cfg.PollHoldSeconds) * time.Second,
		GlobalInFlight: cfg.GlobalInFlight,
	})

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: server.Handler(),
	}

	shutdownContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-shutdownContext.Done()
		server.StartDrain()
		shutdown, cancel := context.WithTimeout(context.Background(), 115*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}
