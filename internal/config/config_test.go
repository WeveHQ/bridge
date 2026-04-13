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

func TestParseEdgeConfigRejectsOutOfRangeInts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		inputs  EdgeInputs
		wantMsg string
	}{
		{
			name:    "poll concurrency zero",
			inputs:  EdgeInputs{Token: "t", HubURL: "https://h", PollConcurrency: "0"},
			wantMsg: "poll concurrency must be >= 1",
		},
		{
			name:    "poll concurrency negative",
			inputs:  EdgeInputs{Token: "t", HubURL: "https://h", PollConcurrency: "-1"},
			wantMsg: "poll concurrency must be >= 1",
		},
		{
			name:    "heartbeat seconds zero",
			inputs:  EdgeInputs{Token: "t", HubURL: "https://h", HeartbeatSeconds: "0"},
			wantMsg: "heartbeat seconds must be >= 1",
		},
		{
			name:    "poll timeout ms zero",
			inputs:  EdgeInputs{Token: "t", HubURL: "https://h", PollTimeoutMS: "0"},
			wantMsg: "poll timeout ms must be >= 1",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseEdgeConfig(tc.inputs)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("expected error containing %q, got %v", tc.wantMsg, err)
			}
		})
	}
}

func TestParseHubConfigRejectsOutOfRangeInts(t *testing.T) {
	t.Parallel()

	base := HubInputs{
		TokenVerifierURL:    "http://127.0.0.1:8181/verify",
		TokenVerifierSecret: "verifier-secret",
		HubSecret:           "internal-secret",
	}

	cases := []struct {
		name    string
		mutate  func(*HubInputs)
		wantMsg string
	}{
		{
			name:    "global in-flight zero",
			mutate:  func(h *HubInputs) { h.GlobalInFlight = "0" },
			wantMsg: "global in-flight must be >= 1",
		},
		{
			name:    "verify timeout ms zero",
			mutate:  func(h *HubInputs) { h.VerifyTimeoutMS = "0" },
			wantMsg: "verify timeout ms must be >= 1",
		},
		{
			name:    "poll hold seconds zero",
			mutate:  func(h *HubInputs) { h.PollHoldSeconds = "0" },
			wantMsg: "poll hold seconds must be >= 1",
		},
		{
			name:    "per-edge max poll concurrency negative",
			mutate:  func(h *HubInputs) { h.PerEdgeMaxPollConcurrency = "-1" },
			wantMsg: "per-edge max poll concurrency must be >= 0",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inputs := base
			tc.mutate(&inputs)
			_, err := ParseHubConfig(inputs)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("expected error containing %q, got %v", tc.wantMsg, err)
			}
		})
	}
}

func TestParseHubConfigAcceptsZeroVerifyCacheSeconds(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHubConfig(HubInputs{
		TokenVerifierURL:    "http://127.0.0.1:8181/verify",
		TokenVerifierSecret: "verifier-secret",
		HubSecret:           "internal-secret",
		VerifyCacheSeconds:  "0",
	})
	if err != nil {
		t.Fatalf("parse hub config: %v", err)
	}
	if cfg.VerifyCacheSeconds != 0 {
		t.Fatalf("unexpected verify cache seconds: %d", cfg.VerifyCacheSeconds)
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
