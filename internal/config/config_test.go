package config

import (
	"strings"
	"testing"
)

func TestParseEdgeConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseEdgeConfig(EdgeInputs{
		Token:            "token",
		HubURL:           "https://hub.example",
		PollConcurrency:  "6",
		HeartbeatSeconds: "10",
		PollTimeoutMS:    "15000",
	})
	if err != nil {
		t.Fatalf("parse edge config: %v", err)
	}

	if cfg.PollConcurrency != 6 {
		t.Fatalf("unexpected poll concurrency: %d", cfg.PollConcurrency)
	}
	if cfg.PollTimeoutMS != 15000 {
		t.Fatalf("unexpected poll timeout: %d", cfg.PollTimeoutMS)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("unexpected log level: %s", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Fatalf("unexpected log format: %s", cfg.Log.Format)
	}
}

func TestParseHubConfigDefaultsListenAddr(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHubConfig(HubInputs{
		TokenVerifierURL:    "http://127.0.0.1:8181/verify",
		TokenVerifierSecret: "verifier-secret",
		HubSecret:           "internal-secret",
	})
	if err != nil {
		t.Fatalf("parse hub config: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Fatalf("unexpected listen addr: %s", cfg.ListenAddr)
	}
	if cfg.TokenVerifierURL != "http://127.0.0.1:8181/verify" {
		t.Fatalf("unexpected token verifier url: %s", cfg.TokenVerifierURL)
	}
	if cfg.TokenVerifierSecret != "verifier-secret" {
		t.Fatalf("unexpected token verifier secret: %s", cfg.TokenVerifierSecret)
	}
	if cfg.VerifyTimeoutMS != 2000 {
		t.Fatalf("unexpected verify timeout: %d", cfg.VerifyTimeoutMS)
	}
	if cfg.VerifyCacheSeconds != 30 {
		t.Fatalf("unexpected verify cache seconds: %d", cfg.VerifyCacheSeconds)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("unexpected log level: %s", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Fatalf("unexpected log format: %s", cfg.Log.Format)
	}
}

func TestParseEdgeConfigRejectsInvalidLogLevel(t *testing.T) {
	t.Parallel()

	_, err := ParseEdgeConfig(EdgeInputs{
		Token:  "token",
		HubURL: "https://hub.example",
		Log: LogInputs{
			Level: "verbose",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "parse log level") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseHubConfigRejectsInvalidLogFormat(t *testing.T) {
	t.Parallel()

	_, err := ParseHubConfig(HubInputs{
		TokenVerifierURL:    "http://127.0.0.1:8181/verify",
		TokenVerifierSecret: "verifier-secret",
		HubSecret:           "internal-secret",
		Log: LogInputs{
			Format: "pretty",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "parse log format") {
		t.Fatalf("unexpected error: %v", err)
	}
}
