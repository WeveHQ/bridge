package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/WeveHQ/bridge/internal/build"
	"github.com/WeveHQ/bridge/internal/wire"
)

func (runner *Runner) runPollSlot(ctx context.Context, errCh chan<- error) {
	for {
		dispatch, ok, err := runner.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			if errors.Is(err, errPollRateLimited) {
				runner.markPollFailure(err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(pollRateLimitedBackoff):
					continue
				}
			}

			runner.markPollFailure(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}
		runner.markPollSuccess()
		if !ok {
			runner.logger.Debug("poll returned no work")
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}

		go runner.runPollSlot(ctx, errCh)
		runner.inFlight.Add(1)
		if err := runner.handleDispatch(ctx, dispatch); err != nil {
			runner.logger.Warn("dispatch response post failed",
				"outboundTraceId", dispatch.OutboundTraceID,
				"error", err,
			)
		}
		runner.inFlight.Add(-1)
		return
	}
}

func (runner *Runner) poll(ctx context.Context) (wire.PollResponse, bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, runner.pollTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, runner.hubURL+wire.PollPath, bytes.NewReader(wire.MustJSON(wire.PollRequest{
		BridgeVersion: build.Version,
	})))
	if err != nil {
		return wire.PollResponse{}, false, err
	}
	runner.decorateAuth(request)

	response, err := runner.client.Do(request)
	if err != nil {
		return wire.PollResponse{}, false, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNoContent {
		return wire.PollResponse{}, false, nil
	}
	if response.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, response.Body)
		return wire.PollResponse{}, false, errPollRateLimited
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return wire.PollResponse{}, false, errors.New(string(body))
	}

	var parsed wire.PollResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return wire.PollResponse{}, false, err
	}

	return parsed, true, nil
}

func (runner *Runner) markPollFailure(err error) {
	runner.stateMu.Lock()
	shouldLog := !runner.pollFailing
	runner.pollFailing = true
	runner.stateMu.Unlock()

	message := "poll failed"
	retryIn := retryInterval
	if errors.Is(err, errPollRateLimited) {
		message = "poll rate limited by hub"
		retryIn = pollRateLimitedBackoff
	}

	if shouldLog {
		runner.logger.Warn(message,
			"error", err,
			"retryIn", retryIn.String(),
		)
	}
}

func (runner *Runner) markPollSuccess() {
	runner.stateMu.Lock()
	recovered := runner.pollFailing
	runner.pollFailing = false
	runner.stateMu.Unlock()

	if recovered {
		runner.logger.Info("polling recovered", "hubURL", runner.hubURL)
	}
}

func (runner *Runner) postResponse(ctx context.Context, response wire.HttpResponse) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.hubURL+wire.ResponsePathPrefix+response.OutboundTraceID, bytes.NewReader(wire.MustJSON(response)))
	if err != nil {
		return err
	}
	runner.decorateAuth(request)

	httpResponse, err := runner.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResponse.Body)
		return errors.New(string(body))
	}

	return nil
}

func (runner *Runner) decorateAuth(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+runner.token)
	request.Header.Set("Content-Type", "application/json")
}
