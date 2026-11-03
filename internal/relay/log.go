package relay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// LevelTrace marks records with no troubleshooting value: successful catalog
// and token-count requests.
const LevelTrace = slog.LevelDebug - 4

// Request outcomes. Every request ends in exactly one; stats count them.
const (
	outcomeOK           = "ok"
	outcomeRejected     = "rejected"             // local validation or authentication failed
	outcomeBusy         = "busy"                 // all forwarding slots taken, or draining
	outcomeUpstreamErr  = "upstream_error"       // upstream answered with an error
	outcomeUnreachable  = "upstream_unreachable" // connection, TLS or protocol failure
	outcomeTimeout      = "timeout"              // no upstream response headers in time
	outcomeBroken       = "upstream_broken"      // upstream cut the response after it began
	outcomeClientClosed = "client_closed"        // the client left before the response ended
)

var logLevels = map[string]slog.Level{
	"trace": LevelTrace, "debug": slog.LevelDebug, "info": slog.LevelInfo,
	"warn": slog.LevelWarn, "error": slog.LevelError,
}

func parseLogLevel(name string) (slog.Level, error) {
	level, ok := logLevels[name]
	if !ok {
		return 0, errors.New("log level must be trace, debug, info, warn or error")
	}
	return level, nil
}

// NameLevel is a slog ReplaceAttr function that prints TRACE instead of DEBUG-4.
func NameLevel(_ []string, attr slog.Attr) slog.Attr {
	if attr.Key == slog.LevelKey {
		if level, ok := attr.Value.Any().(slog.Level); ok && level == LevelTrace {
			attr.Value = slog.StringValue("TRACE")
		}
	}
	return attr
}

func boundedLabel(value string) string {
	const maxLabelBytes = 128
	if len(value) > maxLabelBytes {
		return value[:maxLabelBytes] + "..."
	}
	return value
}

// requestLevel keeps routine success out of default logs: problems are WARN,
// events worth a glance are INFO, plain success is DEBUG or TRACE.
func requestLevel(kind, result string, retries int) slog.Level {
	switch {
	case result == outcomeOK && retries > 0, result == outcomeClientClosed:
		return slog.LevelInfo
	case result == outcomeOK && kind == kindMessages:
		return slog.LevelDebug
	case result == outcomeOK:
		return LevelTrace
	default:
		return slog.LevelWarn
	}
}

// requestOutcome classifies a finished request. aborted means the response broke
// after its headers were sent; clientGone means the client context ended.
func requestOutcome(w *responseSummary, aborted, clientGone bool) string {
	switch {
	case aborted && (clientGone || w.writeFailed), w.status == statusClientClosed:
		return outcomeClientClosed
	case aborted:
		return outcomeBroken
	case w.category != "":
		switch w.status {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return outcomeBusy
		case http.StatusBadGateway:
			return outcomeUnreachable
		case http.StatusGatewayTimeout:
			return outcomeTimeout
		}
		return outcomeRejected
	case w.forwarded && w.status >= http.StatusBadRequest, w.usage != nil && w.usage.errorType != "":
		return outcomeUpstreamErr
	}
	return outcomeOK
}

// logRequest writes one access record. Optional fields appear only when set.
func (s *Server) logRequest(ctx context.Context, w *responseSummary, method, kind, result string, elapsed time.Duration) {
	level := requestLevel(kind, result, w.retries)
	if s.logger == nil || !s.logger.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{
		slog.String("profile", s.config.profile), slog.String("method", methodKind(method)),
		slog.String("path", kind), slog.Int("status", w.status), slog.String("outcome", result),
		slog.Duration("elapsed", elapsed),
		slog.Int("request_bytes", w.requestBytes), slog.Int("response_bytes", w.bytes),
	}
	add := func(key, value string) {
		if value != "" {
			attrs = append(attrs, slog.String(key, value))
		}
	}
	add("model", w.model)
	add("upstream", w.upstream)
	add("category", w.category)
	if w.ttfb > 0 {
		attrs = append(attrs, slog.Duration("ttfb", w.ttfb))
	}
	if w.retries > 0 {
		attrs = append(attrs, slog.Int("retries", w.retries))
		add("retry_reasons", strings.Join(w.retryReasons, ","))
	}
	add("upstream_request_id", w.requestID)
	if tap := w.usage; tap != nil {
		add("error_type", tap.errorType)
		add("stop_reason", tap.stopReason)
		if tap.seen {
			attrs = append(attrs, slog.Int64("input_tokens", tap.tokens.input), slog.Int64("output_tokens", tap.tokens.output),
				slog.Int64("cache_read_tokens", tap.tokens.cacheRead), slog.Int64("cache_write_tokens", tap.tokens.cacheWrite))
		}
	}
	s.logger.LogAttrs(ctx, level, "request", attrs...)
}
