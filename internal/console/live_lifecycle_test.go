package console

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLiveViewRetainsFinalLifecycleMessage(t *testing.T) {
	for _, message := range []string{"stopped", "already running"} {
		t.Run(message, func(t *testing.T) {
			var output bytes.Buffer
			handler := New(&output, false, slog.LevelDebug)
			handler.startLive()
			slog.New(handler).Info(message, "profile", "personal", "mode", "manual")
			handler.Close()
			_, after, ok := strings.Cut(output.String(), "\x1b[?1049l")
			if !ok || !strings.Contains(after, strings.ToUpper(message)) {
				t.Fatalf("final lifecycle message lost: %q", output.String())
			}
		})
	}
}
