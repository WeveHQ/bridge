package edge

import (
	"context"
	"net/url"

	"github.com/WeveHQ/bridge/internal/wire"
)

func (runner *Runner) handleDispatch(ctx context.Context, dispatch wire.PollResponse) error {
	runner.logger.Debug("dispatch received",
		"outboundTraceId", dispatch.OutboundTraceID,
		"method", dispatch.Req.Method,
		"url", dispatch.Req.URL,
	)

	response := runner.executor.Execute(dispatch.OutboundTraceID, dispatch.Req)
	if err := runner.postResponse(ctx, response); err != nil {
		return err
	}

	attrs := runner.dispatchLogAttrs(dispatch, response)
	if response.Meta.Error != nil {
		runner.logger.Warn("dispatch completed with execution error", attrs...)
		return nil
	}

	runner.logger.Info("dispatch completed", attrs...)
	return nil
}

func (runner *Runner) dispatchLogAttrs(dispatch wire.PollResponse, response wire.HttpResponse) []any {
	attrs := []any{
		"outboundTraceId", dispatch.OutboundTraceID,
		"method", dispatch.Req.Method,
		"url", dispatch.Req.URL,
		"durationMs", response.Meta.DurationMs,
		"bytesOut", response.Meta.BytesOut,
		"bytesIn", response.Meta.BytesIn,
	}

	if parsedURL, err := url.Parse(dispatch.Req.URL); err == nil {
		attrs = append(attrs, "targetHost", parsedURL.Hostname())
	}

	if response.Status != 0 {
		attrs = append(attrs, "status", response.Status)
	}

	if response.Meta.Error != nil {
		attrs = append(attrs,
			"errorKind", response.Meta.Error.Kind,
			"errorMessage", response.Meta.Error.Message,
			"outcome", "execution_error",
		)
		return attrs
	}

	attrs = append(attrs, "outcome", "response_posted")
	return attrs
}
