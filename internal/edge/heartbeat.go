package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/wire"
)

func (runner *Runner) runHeartbeatLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(runner.heartbeatInterval)
	defer ticker.Stop()

	for {
		if err := runner.sendHeartbeat(ctx); err != nil && !errors.Is(err, context.Canceled) {
			runner.markHeartbeatFailure(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}
		runner.markHeartbeatSuccess()
		runner.logger.Debug("heartbeat acknowledged")

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (runner *Runner) sendHeartbeat(ctx context.Context) error {
	hostname, _ := os.Hostname()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.hubURL+wire.HeartbeatPath, bytes.NewReader(wire.MustJSON(wire.HeartbeatRequest{
		BridgeVersion: build.Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		UptimeSec:     uint64(time.Since(runner.startedAt).Seconds()),
		InFlight:      uint32(runner.inFlight.Load()),
		Hostname:      hostname,
	})))
	if err != nil {
		return err
	}
	runner.decorateAuth(request)

	response, err := runner.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return errors.New(string(body))
	}

	return nil
}

func (runner *Runner) markHeartbeatFailure(err error) {
	runner.stateMu.Lock()
	shouldLog := !runner.heartbeatFailing
	runner.heartbeatFailing = true
	runner.stateMu.Unlock()

	if shouldLog {
		runner.logger.Warn("heartbeat failed",
			"error", err,
			"retryIn", retryInterval.String(),
		)
	}
}

func (runner *Runner) markHeartbeatSuccess() {
	runner.stateMu.Lock()
	firstSuccess := !runner.connectedLogged
	recovered := runner.heartbeatFailing
	runner.connectedLogged = true
	runner.heartbeatFailing = false
	runner.stateMu.Unlock()

	if firstSuccess {
		runner.logger.Info("connected to hub", "hubURL", runner.hubURL)
		return
	}
	if recovered {
		runner.logger.Info("heartbeat recovered", "hubURL", runner.hubURL)
	}
}
