// Package console formats bounded proxy diagnostics for an operator terminal.
package console

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Handler prints operator-facing records; it never prints arbitrary attributes.
type Handler struct {
	writer io.Writer
	mutex  *sync.Mutex
	level  slog.Leveler
	color  bool
	attrs  []slog.Attr
	group  string
}

// New creates a human-readable logger sink with explicit color policy and a
// minimum level, which may change while the handler is in use.
func New(writer io.Writer, color bool, level slog.Leveler) *Handler {
	return &Handler{writer: writer, mutex: &sync.Mutex{}, color: color, level: level}
}

// ColorEnabled enables ANSI only for an actual terminal and honors NO_COLOR.
func ColorEnabled(file *os.File) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(file)
}

// Enabled accepts the configured level and higher severity.
func (handler *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= handler.level.Level()
}

// Handle prints only the proxy's known safe diagnostic fields.
func (handler *Handler) Handle(_ context.Context, record slog.Record) error {
	values := map[string]slog.Value{}
	add := func(attr slog.Attr) { values[attr.Key] = attr.Value.Resolve() }
	for _, attr := range handler.attrs {
		add(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		if handler.group == "" {
			add(attr)
		}
		return true
	})
	value := func(key string) string { return printable(values[key].String()) }
	label := strings.ToUpper(record.Message)
	message := ""
	switch record.Message {
	case "starting", "already running":
		mode := value("mode")
		policy := ""
		if mode == "manual" {
			policy = " · auto-close off"
		}
		message = fmt.Sprintf("%s  http://%s  %s%s  pid=%s  %s", value("profile"), value("listen"), mode, policy, value("pid"), value("version"))
	case "configuration valid":
		label = "VALID"
		message = value("profile")
	case "ready":
		message = fmt.Sprintf("%s  http://%s", value("profile"), value("listen"))
	case "request":
		label = value("status")
		message = fmt.Sprintf("%-4s %-12s %-36s %9s", value("method"), value("path"), route(values), duration(values["elapsed"]))
		message += requestDetails(values)
	case "stats":
		message = fmt.Sprintf("%-12s %-36s %s req", value("path"), route(values), value("requests")) + statsDetails(values)
	case "stopped":
		message = value("profile") + "  " + value("mode")
	default:
		label = record.Level.String()
		message = printable(record.Message)
		if category := value("category"); category != "" {
			message += ": " + category
		}
	}
	label = fmt.Sprintf("%-16s", printable(label))
	if handler.color {
		shade := "36"
		switch {
		case record.Level >= slog.LevelError || strings.HasPrefix(label, "5"):
			shade = "31"
		case record.Level >= slog.LevelWarn || strings.HasPrefix(label, "4"):
			shade = "33"
		case strings.TrimSpace(label) == "READY" || strings.HasPrefix(label, "2"):
			shade = "32"
		}
		label = "\x1b[" + shade + "m" + label + "\x1b[0m"
	}
	line := fmt.Sprintf("%s  %s %s\n", record.Time.Format("15:04:05"), label, message)
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	_, err := io.WriteString(handler.writer, line)
	return err
}

// WithAttrs retains known root attributes without mutating the original handler.
func (handler *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copied := *handler
	if handler.group == "" {
		copied.attrs = append(append([]slog.Attr(nil), handler.attrs...), attrs...)
	}
	return &copied
}

// WithGroup scopes attributes; unknown grouped data is intentionally not emitted.
func (handler *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return handler
	}
	copied := *handler
	copied.group = name
	return &copied
}

// text returns an attribute for display, or "" when it is absent.
func text(values map[string]slog.Value, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	return printable(value.String())
}

func route(values map[string]slog.Value) string {
	if upstream := text(values, "upstream"); upstream != "" {
		return text(values, "model") + " → " + upstream
	}
	return text(values, "model")
}

func duration(value slog.Value) string {
	if value.Kind() != slog.KindDuration {
		return printable(value.String())
	}
	if d := value.Duration(); d > 0 && d < time.Millisecond {
		return "<1ms"
	}
	return value.Duration().Round(time.Millisecond).String()
}

func count(values map[string]slog.Value, key string) int64 {
	if value := values[key]; value.Kind() == slog.KindInt64 {
		return value.Int64()
	}
	return 0
}

// requestDetails lists only what is unusual: the failure, slow first byte,
// token use and retries. A plain success adds nothing but token counts.
func requestDetails(values map[string]slog.Value) string {
	var parts []string
	result := text(values, "outcome")
	for _, key := range []string{"outcome", "category", "error_type"} {
		if value := text(values, key); value != "" && value != "ok" {
			parts = append(parts, value)
		}
	}
	if _, ok := values["ttfb"]; ok {
		parts = append(parts, "ttfb "+duration(values["ttfb"]))
	}
	if _, ok := values["input_tokens"]; ok {
		parts = append(parts, fmt.Sprintf("tokens %d→%d", count(values, "input_tokens"), count(values, "output_tokens")))
		if read, write := count(values, "cache_read_tokens"), count(values, "cache_write_tokens"); read+write > 0 {
			parts = append(parts, fmt.Sprintf("cache r%d w%d", read, write))
		}
	}
	switch stop := text(values, "stop_reason"); stop {
	case "", "end_turn", "tool_use", "stop_sequence":
	default:
		parts = append(parts, "stop "+stop)
	}
	if retries := count(values, "retries"); retries > 0 {
		retried := fmt.Sprintf("retried ×%d", retries)
		if reasons := text(values, "retry_reasons"); reasons != "" {
			retried += " (" + reasons + ")"
		}
		parts = append(parts, retried)
	}
	if id := text(values, "upstream_request_id"); id != "" && result != "ok" {
		parts = append(parts, "id "+id)
	}
	return joinParts(parts)
}

func statsDetails(values map[string]slog.Value) string {
	var parts []string
	for _, key := range []string{"failed", "client_closed", "retries"} {
		if n := count(values, key); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(key, "_", " "), n))
		}
	}
	if _, ok := values["ttfb_p50"]; ok {
		parts = append(parts, fmt.Sprintf("ttfb p50 %s p95 %s", duration(values["ttfb_p50"]), duration(values["ttfb_p95"])))
	}
	if _, ok := values["duration_p95"]; ok {
		parts = append(parts, "duration p95 "+duration(values["duration_p95"]))
	}
	if _, ok := values["input_tokens"]; ok {
		parts = append(parts, fmt.Sprintf("tokens %d→%d", count(values, "input_tokens"), count(values, "output_tokens")))
	}
	if _, ok := values["period"]; ok {
		parts = append(parts, "over "+duration(values["period"]))
	}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return "  " + strings.Join(parts, "  ")
}

func printable(value string) string {
	return strings.Map(func(character rune) rune {
		if !unicode.IsPrint(character) {
			return '�'
		}
		return character
	}, value)
}
