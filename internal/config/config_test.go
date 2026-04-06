package config

import "testing"

func TestParseEdgeConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseEdgeConfig(EdgeInputs{
		Token:            "token",
		HubURL:           "https://hub.example",
		BridgeID:         "bridge_123",
		TenantID:         "tenant_123",
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
}

func TestParseHubConfigDefaultsListenAddr(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHubConfig(HubInputs{
		VerifyTokenURL: "http://127.0.0.1:8181/verify",
		InternalSecret: "internal-secret",
	})
	if err != nil {
		t.Fatalf("parse hub config: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Fatalf("unexpected listen addr: %s", cfg.ListenAddr)
	}
	if cfg.VerifyTimeoutMS != 2000 {
		t.Fatalf("unexpected verify timeout: %d", cfg.VerifyTimeoutMS)
	}
	if cfg.VerifyCacheSeconds != 30 {
		t.Fatalf("unexpected verify cache seconds: %d", cfg.VerifyCacheSeconds)
	}
}
