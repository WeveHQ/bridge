package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/WeveHQ/bridge/internal/logging"
)

const (
	defaultPollConcurrency  = 4
	defaultHeartbeatSeconds = 15
	defaultPollTimeoutMs    = 30000
	defaultListenAddr       = ":8080"
	defaultVerifyTimeoutMs  = 2000
	defaultVerifyCacheSec   = 30
	defaultLogLevel         = logging.LevelInfo
	defaultLogFormat        = logging.FormatJSON
)

type LogConfig struct {
	Level  string
	Format string
}

type LogInputs struct {
	Level  string
	Format string
}

type EdgeConfig struct {
	Token            string
	HubURL           string
	PollConcurrency  int
	HeartbeatSeconds int
	PollTimeoutMS    int
	AllowedHosts     []string
	Log              LogConfig
}

type EdgeInputs struct {
	Token            string
	HubURL           string
	PollConcurrency  string
	HeartbeatSeconds string
	PollTimeoutMS    string
	AllowedHosts     string
	Log              LogInputs
}

type HubConfig struct {
	ListenAddr                string
	TokenVerifierURL          string
	TokenVerifierSecret       string
	VerifyTimeoutMS           int
	VerifyCacheSeconds        int
	HubSecret                 string
	PollHoldSeconds           int
	GlobalInFlight            int
	PerEdgeMaxPollConcurrency int
	Log                       LogConfig
}

type HubInputs struct {
	ListenAddr                string
	TokenVerifierURL          string
	TokenVerifierSecret       string
	VerifyTimeoutMS           string
	VerifyCacheSeconds        string
	HubSecret                 string
	PollHoldSeconds           string
	GlobalInFlight            string
	PerEdgeMaxPollConcurrency string
	Log                       LogInputs
}

func ParseEdgeConfig(inputs EdgeInputs) (EdgeConfig, error) {
	token := firstNonEmpty(inputs.Token, os.Getenv("WEVE_BRIDGE_EDGE_TOKEN"))
	if token == "" {
		return EdgeConfig{}, errors.New("missing WEVE_BRIDGE_EDGE_TOKEN")
	}

	hubURL := firstNonEmpty(inputs.HubURL, os.Getenv("WEVE_BRIDGE_EDGE_HUB_URL"))
	if hubURL == "" {
		return EdgeConfig{}, errors.New("missing WEVE_BRIDGE_EDGE_HUB_URL")
	}

	pollConcurrency, err := parseInt(firstNonEmpty(inputs.PollConcurrency, os.Getenv("WEVE_BRIDGE_EDGE_POLL_CONCURRENCY")), defaultPollConcurrency)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse poll concurrency: %w", err)
	}

	heartbeatSeconds, err := parseInt(firstNonEmpty(inputs.HeartbeatSeconds, os.Getenv("WEVE_BRIDGE_EDGE_HEARTBEAT_SECONDS")), defaultHeartbeatSeconds)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse heartbeat seconds: %w", err)
	}

	pollTimeoutMS, err := parseInt(firstNonEmpty(inputs.PollTimeoutMS, os.Getenv("WEVE_BRIDGE_EDGE_POLL_TIMEOUT_MS")), defaultPollTimeoutMs)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("parse poll timeout ms: %w", err)
	}

	allowedHosts := parseAllowedHosts(firstNonEmpty(inputs.AllowedHosts, os.Getenv("WEVE_BRIDGE_EDGE_ALLOWED_HOSTS")))
	logCfg, err := parseLogConfig(inputs.Log)
	if err != nil {
		return EdgeConfig{}, err
	}

	return EdgeConfig{
		Token:            token,
		HubURL:           strings.TrimRight(hubURL, "/"),
		PollConcurrency:  pollConcurrency,
		HeartbeatSeconds: heartbeatSeconds,
		PollTimeoutMS:    pollTimeoutMS,
		AllowedHosts:     allowedHosts,
		Log:              logCfg,
	}, nil
}

func parseAllowedHosts(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts))
	for _, part := range parts {
		host := strings.ToLower(strings.TrimSpace(part))
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	return hosts
}

func ParseHubConfig(inputs HubInputs) (HubConfig, error) {
	listenAddr := firstNonEmpty(inputs.ListenAddr, os.Getenv("WEVE_BRIDGE_HUB_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	verifyTokenURL := firstNonEmpty(inputs.TokenVerifierURL, os.Getenv("WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL"))
	if verifyTokenURL == "" {
		return HubConfig{}, errors.New("missing WEVE_BRIDGE_HUB_TOKEN_VERIFIER_URL")
	}

	verifyTokenSecret := firstNonEmpty(inputs.TokenVerifierSecret, os.Getenv("WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET"))
	if verifyTokenSecret == "" {
		return HubConfig{}, errors.New("missing WEVE_BRIDGE_HUB_TOKEN_VERIFIER_SECRET")
	}

	hubSecret := firstNonEmpty(inputs.HubSecret, os.Getenv("WEVE_BRIDGE_HUB_SECRET"))
	if hubSecret == "" {
		return HubConfig{}, errors.New("missing WEVE_BRIDGE_HUB_SECRET")
	}

	verifyTimeoutMS, err := parseInt(firstNonEmpty(inputs.VerifyTimeoutMS, os.Getenv("WEVE_BRIDGE_HUB_VERIFY_TIMEOUT_MS")), defaultVerifyTimeoutMs)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse verify timeout ms: %w", err)
	}

	verifyCacheSeconds, err := parseInt(firstNonEmpty(inputs.VerifyCacheSeconds, os.Getenv("WEVE_BRIDGE_HUB_VERIFY_CACHE_SECONDS")), defaultVerifyCacheSec)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse verify cache seconds: %w", err)
	}

	pollHoldSeconds, err := parseInt(firstNonEmpty(inputs.PollHoldSeconds, os.Getenv("WEVE_BRIDGE_HUB_POLL_HOLD_SECONDS")), 25)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse poll hold seconds: %w", err)
	}

	globalInFlight, err := parseInt(firstNonEmpty(inputs.GlobalInFlight, os.Getenv("WEVE_BRIDGE_HUB_GLOBAL_IN_FLIGHT")), 64)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse global in-flight: %w", err)
	}

	perEdgeMaxPollConcurrency, err := parseInt(firstNonEmpty(inputs.PerEdgeMaxPollConcurrency, os.Getenv("WEVE_BRIDGE_HUB_PER_EDGE_MAX_POLL_CONCURRENCY")), 0)
	if err != nil {
		return HubConfig{}, fmt.Errorf("parse per-edge max poll concurrency: %w", err)
	}
	if perEdgeMaxPollConcurrency < 0 {
		return HubConfig{}, errors.New("per-edge max poll concurrency must be non-negative")
	}

	logCfg, err := parseLogConfig(inputs.Log)
	if err != nil {
		return HubConfig{}, err
	}

	return HubConfig{
		ListenAddr:                listenAddr,
		TokenVerifierURL:          strings.TrimRight(verifyTokenURL, "/"),
		TokenVerifierSecret:       verifyTokenSecret,
		VerifyTimeoutMS:           verifyTimeoutMS,
		VerifyCacheSeconds:        verifyCacheSeconds,
		HubSecret:                 hubSecret,
		PollHoldSeconds:           pollHoldSeconds,
		GlobalInFlight:            globalInFlight,
		PerEdgeMaxPollConcurrency: perEdgeMaxPollConcurrency,
		Log:                       logCfg,
	}, nil
}

func parseLogConfig(inputs LogInputs) (LogConfig, error) {
	level := firstNonEmpty(inputs.Level, os.Getenv("WEVE_BRIDGE_LOG_LEVEL"))
	if level == "" {
		level = defaultLogLevel
	}
	if _, err := logging.ParseLevel(level); err != nil {
		return LogConfig{}, fmt.Errorf("parse log level: %w", err)
	}

	format := firstNonEmpty(inputs.Format, os.Getenv("WEVE_BRIDGE_LOG_FORMAT"))
	if format == "" {
		format = defaultLogFormat
	}
	if _, err := logging.ParseFormat(format); err != nil {
		return LogConfig{}, fmt.Errorf("parse log format: %w", err)
	}

	return LogConfig{
		Level:  strings.ToLower(strings.TrimSpace(level)),
		Format: strings.ToLower(strings.TrimSpace(format)),
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
