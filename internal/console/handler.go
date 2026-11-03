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
	color  bool
	attrs  []slog.Attr
	group  string
}

// New creates a human-readable logger sink with explicit color policy.
func New(writer io.Writer, color bool) *Handler {
	return &Handler{writer: writer, mutex: &sync.Mutex{}, color: color}
}

// ColorEnabled enables ANSI only for an actual terminal and honors NO_COLOR.
func ColorEnabled(file *os.File) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(file)
}

// Enabled accepts info and higher severity.
func (*Handler) Enabled(_ context.Context, level slog.Level) bool { return level >= slog.LevelInfo }

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
		route := value("model")
		if upstream := value("upstream"); upstream != "" {
			route += " → " + upstream
		}
		elapsed := value("elapsed")
		if duration := values["elapsed"]; duration.Kind() == slog.KindDuration {
			elapsed = duration.Duration().Round(time.Millisecond).String()
			if duration.Duration() > 0 && duration.Duration() < time.Millisecond {
				elapsed = "<1ms"
			}
		}
		message = fmt.Sprintf("%-4s %-12s %-36s %9s", value("method"), value("path"), route, elapsed)
		if category := value("category"); category != "" && category != "<nil>" {
			message += "  " + category
		}
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

func printable(value string) string {
	return strings.Map(func(character rune) rune {
		if !unicode.IsPrint(character) {
			return '�'
		}
		return character
	}, value)
}
