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
	logger := slog.New(New(&output, false))
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

func TestColorAndTerminalInjectionSafety(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(New(&output, true))
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
	logger := slog.New(New(&output, false)).With("authorization", "private-value", "local_token", "private-local-key")
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
	handler := New(&output, false)
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
