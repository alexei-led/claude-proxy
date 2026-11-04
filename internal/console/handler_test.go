package console

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadableManualLifecycleAndRequestLogs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, false, slog.LevelInfo))
	logger.Info("starting", "profile", "personal", "listen", "127.0.0.1:43171", "pid", 42, "mode", "manual", "version", "v-test")
	logger.Info("ready", "profile", "personal", "listen", "127.0.0.1:43171")
	logger.Info("request", "method", "POST", "path", "messages", "model", "native-model", "upstream", "worker", "status", 200, "elapsed", 123*time.Millisecond, "category", "")
	logger.Info("stopped", "profile", "personal", "mode", "manual")
	text := output.String()
	for _, want := range []string{"STARTING", "READY", "STOPPED", "auto-close off", "http://127.0.0.1:43171", "native-model → worker", "123ms", "200"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing readable field %q in %s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") || strings.Contains(text, `"msg":`) {
		t.Fatal("non-terminal logs were colored or JSON")
	}
}

func TestRetriedRequestsAreMarked(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, false, slog.LevelInfo))
	logger.Info("request", "method", "POST", "path", "messages", "model", "m", "upstream", "u", "status", 200, "elapsed", time.Second, "retries", 2)
	logger.Info("request", "method", "POST", "path", "messages", "model", "m", "upstream", "u", "status", 200, "elapsed", time.Second, "retries", 0)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if !strings.Contains(lines[0], "retried ×2") || strings.Contains(lines[1], "retried") {
		t.Fatalf("retry marker wrong: %q", lines)
	}
}

func TestColorAndTerminalInjectionSafety(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, true, slog.LevelInfo))
	logger.Info("request", "method", "POST", "path", "messages", "model", "native\x1b[2J\nspoof", "upstream", "worker", "status", 502, "elapsed", time.Second)
	text := output.String()
	if !strings.Contains(text, "\x1b[31m502") || !strings.Contains(text, "\x1b[0m") {
		t.Fatal("error status color missing")
	}
	if strings.Contains(text, "\x1b[2J") || strings.Count(text, "\n") != 1 {
		t.Fatal("untrusted labels injected terminal controls or log lines")
	}
}

func TestUnknownAttributesNeverExposeCredentials(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, false, slog.LevelInfo)).With("authorization", "private-value", "local_token", "private-local-key")
	logger.Info("ready", "profile", "work", "listen", "127.0.0.1:43172")
	if strings.Contains(output.String(), "private-") {
		t.Fatal("unknown credentials were logged")
	}
}

func TestColorDisabledForNonTerminalsAndNoColor(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if ColorEnabled(file) {
		t.Fatal("regular output file was considered a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(os.Stderr) {
		t.Fatal("NO_COLOR was ignored")
	}
}

func TestHandlerAttributesDoNotMutateParent(t *testing.T) {
	var output bytes.Buffer
	handler := New(&output, false, slog.LevelInfo)
	with := handler.WithAttrs([]slog.Attr{slog.String("profile", "personal")})
	record := slog.NewRecord(time.Unix(0, 0), slog.LevelInfo, "ready", 0)
	record.Add("listen", "127.0.0.1:1")
	if err := with.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "personal") {
		t.Fatal("child attributes mutated parent handler")
	}
}

func TestRequestDetailsShowOnlyWhatMatters(t *testing.T) {
	const outcome = "outcome"
	cases := []struct {
		name   string
		attrs  []any
		want   []string
		absent []string
	}{
		{"plain success", []any{outcome, "ok", "ttfb", 820 * time.Millisecond, "input_tokens", int64(12), "output_tokens", int64(56),
			"stop_reason", "end_turn", "upstream_request_id", "req_1"},
			[]string{"ttfb 820ms", "tokens 12→56"}, []string{" ok", "end_turn", "req_1", "cache"}},
		{"broken stream", []any{outcome, "upstream_broken", "upstream_request_id", "req_2"},
			[]string{"upstream_broken", "id req_2"}, nil},
		{"provider error", []any{outcome, "upstream_error", "error_type", "overloaded_error", "retries", int64(2), "retry_reasons", "529,529"},
			[]string{"upstream_error  overloaded_error", "retried ×2 (529,529)"}, nil},
		{"truncated output", []any{outcome, "ok", "stop_reason", "max_tokens", "input_tokens", int64(1), "output_tokens", int64(2),
			"cache_read_tokens", int64(300), "cache_write_tokens", int64(4)},
			[]string{"stop max_tokens", "cache r300 w4"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(New(&output, false, slog.LevelInfo))
			attrs := append([]any{"method", "POST", "path", "messages", "model", "m", "upstream", "u", "status", 200, "elapsed", time.Second}, tc.attrs...)
			logger.Info("request", attrs...)
			for _, want := range tc.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in %q", want, output.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(output.String(), absent) {
					t.Errorf("unexpected %q in %q", absent, output.String())
				}
			}
		})
	}
}

func TestStatsLine(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, false, slog.LevelInfo))
	logger.Info("stats", "path", "messages", "model", "m", "upstream", "u", "period", 5*time.Minute, "requests", int64(9),
		"failed", int64(2), "client_closed", int64(0), "retries", int64(1), "ttfb_p50", time.Second, "ttfb_p95", 5*time.Second,
		"duration_p95", 30*time.Second, "input_tokens", int64(100), "output_tokens", int64(50),
		"registered_clients", 2, "active_requests", 0, "tracking_uncertain", true)
	for _, want := range []string{"STATS", "m → u", "9 req", "failed 2", "retries 1", "ttfb p50 1s p95 5s", "duration p95 30s", "tokens 100→50", "over 5m0s", "2 registered sessions", "0 active requests", "tracking uncertain", "manual clients not counted"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %q", want, output.String())
		}
	}
	if strings.Contains(output.String(), "client closed") {
		t.Errorf("zero count shown: %q", output.String())
	}
}

func TestLevelGateFollowsLevelVar(t *testing.T) {
	var output bytes.Buffer
	level := new(slog.LevelVar)
	level.Set(slog.LevelDebug)
	logger := slog.New(New(&output, false, level))
	logger.Debug("request", "status", 200)
	level.Set(slog.LevelWarn)
	logger.Debug("request", "status", 201)
	logger.Info("ready", "profile", "p", "listen", "127.0.0.1:1")
	if text := output.String(); !strings.Contains(text, "200") || strings.Contains(text, "201") || strings.Contains(text, "READY") {
		t.Fatalf("level gate wrong: %q", text)
	}
}

func TestSubMillisecondRequestsRemainReadable(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, false, slog.LevelInfo))
	logger.Info("request", "method", "GET", "path", "models", "status", 200, "model", "", "upstream", "", "elapsed", 100*time.Microsecond, "category", "")
	if !strings.Contains(output.String(), "<1ms") {
		t.Fatal("short request was displayed as zero seconds")
	}
}
