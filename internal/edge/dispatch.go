package edge

import (
	"context"
	"net/url"
	"time"

	"github.com/WeveHQ/bridge/internal/wire"
)

func (runner *Runner) handleDispatch(ctx context.Context, dispatch wire.PollResponse) error {
	runner.logger.Debug("dispatch received",
		"outboundTraceId", dispatch.OutboundTraceID,
		"method", dispatch.Req.Method,
		"operation", dispatch.Operation,
		"targetHost", dispatchTargetHost(dispatch),
	)

	var response wire.HttpResponse
	if err := dispatch.Validate(); err != nil {
		response = newErrorResponse(dispatch.OutboundTraceID, time.Now(), 0, invalidRequest(err))
	} else if dispatch.Operation == wire.OperationTLSPreflight {
		response = runner.executor.Preflight(ctx, dispatch.OutboundTraceID, *dispatch.Preflight)
	} else {
		response = runner.executor.Execute(dispatch.OutboundTraceID, dispatch.Req)
	}
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
		"operation", dispatch.Operation,
		"targetHost", dispatchTargetHost(dispatch),
		"durationMs", response.Meta.DurationMs,
		"bytesOut", response.Meta.BytesOut,
		"bytesIn", response.Meta.BytesIn,
	}

	if response.Status != 0 {
		attrs = append(attrs, "status", response.Status)
	}

	if response.Meta.Error != nil {
		attrs = append(attrs,
			"errorKind", response.Meta.Error.Kind,
			"errorCode", response.Meta.Error.Code,
			"errorMessage", response.Meta.Error.Message,
			"outcome", "execution_error",
		)
		return attrs
	}

	attrs = append(attrs, "outcome", "response_posted")
	return attrs
}

func dispatchTargetHost(dispatch wire.PollResponse) string {
	if dispatch.Preflight != nil {
		return dispatch.Preflight.Hostname
	}
	if u, err := url.Parse(dispatch.Req.URL); err == nil {
		return u.Hostname()
	}
	return ""
}
