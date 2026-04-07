package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultPollConcurrency  = 4
	defaultHeartbeatSeconds = 15
	defaultPollTimeoutMs    = 30000
	defaultListenAddr       = ":8080"
	defaultVerifyTimeoutMs  = 2000
	defaultVerifyCacheSec   = 30
)

type EdgeConfig struct {
	Token            string
	HubURL           string
	PollConcurrency  int
	HeartbeatSeconds int
	PollTimeoutMS    int
}

type EdgeInputs struct {
	Token            string
	HubURL           string
	PollConcurrency  string
	HeartbeatSeconds string
	PollTimeoutMS    string
}

type HubConfig struct {
	ListenAddr         string
	VerifyTokenURL     string
	VerifyTimeoutMS    int
	VerifyCacheSeconds int
	InternalSecret     string
	PollHoldSeconds    int
	GlobalInFlight     int
}

type HubInputs struct {
	ListenAddr         string
	VerifyTokenURL     string
	VerifyTimeoutMS    string
	VerifyCacheSeconds string
	InternalSecret     string
	PollHoldSeconds    string
	GlobalInFlight     string
}

func ParseEdgeConfig(inputs EdgeInputs) (EdgeConfig, error) {
	token := firstNonEmpty(inputs.Token, os.Getenv("WEVE_BRIDGE_TOKEN"))
	if token == "" {
		return EdgeConfig{}, errors.New("missing WEVE_BRIDGE_TOKEN")
	}

	hubURL := firstNonEmpty(inputs.HubURL, os.Getenv("WEVE_BRIDGE_HUB_URL"))
	if hubURL == "" {
		return EdgeConfig{}, errors.New("missing WEVE_BRIDGE_HUB_URL")
	}

	pollConcurrency, err := parseInt(firstNonEmpty(inputs.PollConcurrency, os.Getenv("WEVE_BRIDGE_POLL_CONCURRENCY")), defaultPollConcurrency)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse poll concurrency: %w", err)
	}

	heartbeatSeconds, err := parseInt(firstNonEmpty(inputs.HeartbeatSeconds, os.Getenv("WEVE_BRIDGE_HEARTBEAT_SECONDS")), defaultHeartbeatSeconds)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse heartbeat seconds: %w", err)
	}

	pollTimeoutMS, err := parseInt(firstNonEmpty(inputs.PollTimeoutMS, os.Getenv("WEVE_BRIDGE_POLL_TIMEOUT_MS")), defaultPollTimeoutMs)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse poll timeout ms: %w", err)
	}

	return EdgeConfig{
		Token:            token,
		HubURL:           strings.TrimRight(hubURL, "/"),
		PollConcurrency:  pollConcurrency,
		HeartbeatSeconds: heartbeatSeconds,
		PollTimeoutMS:    pollTimeoutMS,
	}, nil
}

func ParseHubConfig(inputs HubInputs) (HubConfig, error) {
	listenAddr := firstNonEmpty(inputs.ListenAddr, os.Getenv("WEVE_BRIDGE_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	verifyTokenURL := firstNonEmpty(inputs.VerifyTokenURL, os.Getenv("WEVE_BRIDGE_VERIFY_TOKEN_URL"))
	if verifyTokenURL == "" {
		return HubConfig{}, errors.New("missing WEVE_BRIDGE_VERIFY_TOKEN_URL")
	}

	internalSecret := firstNonEmpty(inputs.InternalSecret, os.Getenv("WEVE_BRIDGE_INTERNAL_SECRET"))
	if internalSecret == "" {
		return HubConfig{}, errors.New("missing WEVE_BRIDGE_INTERNAL_SECRET")
	}

	verifyTimeoutMS, err := parseInt(firstNonEmpty(inputs.VerifyTimeoutMS, os.Getenv("WEVE_BRIDGE_VERIFY_TIMEOUT_MS")), defaultVerifyTimeoutMs)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse verify timeout ms: %w", err)
	}

	verifyCacheSeconds, err := parseInt(firstNonEmpty(inputs.VerifyCacheSeconds, os.Getenv("WEVE_BRIDGE_VERIFY_CACHE_SECONDS")), defaultVerifyCacheSec)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse verify cache seconds: %w", err)
	}

	pollHoldSeconds, err := parseInt(firstNonEmpty(inputs.PollHoldSeconds, os.Getenv("WEVE_BRIDGE_POLL_HOLD_SECONDS")), 25)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse poll hold seconds: %w", err)
	}

	globalInFlight, err := parseInt(firstNonEmpty(inputs.GlobalInFlight, os.Getenv("WEVE_BRIDGE_GLOBAL_IN_FLIGHT")), 64)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse global in-flight: %w", err)
	}

	return HubConfig{
		ListenAddr:         listenAddr,
		VerifyTokenURL:     strings.TrimRight(verifyTokenURL, "/"),
		VerifyTimeoutMS:    verifyTimeoutMS,
		VerifyCacheSeconds: verifyCacheSeconds,
		InternalSecret:     internalSecret,
		PollHoldSeconds:    pollHoldSeconds,
		GlobalInFlight:     globalInFlight,
	}, nil
}

func parseInt(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}

	return value, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
