package console

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestLiveViewClearsTrafficWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		handler := New(&output, false, slog.LevelDebug)
		handler.startLive()
		logger := slog.New(handler)
		handler.Activity(2, false, 1)
		logger.Debug("request", "status", 200, "path", "messages", "model", "test-model", "elapsed", time.Second)
		handler.Activity(2, false, 0)
		if !strings.Contains(lastFrame(output.String()), "test-model") {
			t.Fatal("recent traffic missing")
		}
		time.Sleep(30 * time.Second)
		handler.Activity(2, false, 0)
		frame := lastFrame(output.String())
		for _, want := range []string{"IDLE", "2 registered sessions", "0 active requests", "manual clients not counted"} {
			if !strings.Contains(frame, want) {
				t.Errorf("missing %q in %q", want, frame)
			}
		}
		if strings.Contains(frame, "test-model") {
			t.Fatal("idle view retained old traffic")
		}
		output.Reset()
		handler.Activity(2, false, 0)
		logger.Info("stats", "path", "messages", "model", "test-model", "requests", int64(1))
		if output.Len() != 0 {
			t.Fatal("idle view repainted without activity")
		}
		handler.Close()
		if !strings.Contains(output.String(), "\x1b[?1049l") {
			t.Fatal("terminal not restored")
		}
	})
}

func TestLiveViewKeepsLongRunningRequestsVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		handler := New(&output, false, slog.LevelDebug)
		handler.startLive()
		handler.Activity(1, true, 1)
		slog.New(handler).Debug("request", "status", 200, "model", "test-model")
		time.Sleep(time.Minute)
		handler.Activity(1, true, 1)
		frame := lastFrame(output.String())
		for _, want := range []string{"ACTIVE", "1 active requests", "tracking uncertain", "test-model"} {
			if !strings.Contains(frame, want) {
				t.Errorf("missing %q in %q", want, frame)
			}
		}
	})
}

func TestNonTerminalActivityDoesNotWriteOrClearLogs(t *testing.T) {
	var output bytes.Buffer
	handler := New(&output, false, slog.LevelInfo)
	handler.StartLive()
	slog.New(handler).Info("request", "status", 200)
	before := output.String()
	handler.Activity(0, false, 0)
	handler.Close()
	if output.String() != before || strings.Contains(before, "\x1b[") {
		t.Fatal("non-terminal log was changed by live display")
	}
}

func TestLiveViewBoundsRecentRecords(t *testing.T) {
	var output bytes.Buffer
	handler := New(&output, false, slog.LevelDebug)
	handler.startLive()
	logger := slog.New(handler)
	for i := 0; i < 100; i++ {
		logger.Debug("request", "status", 200)
	}
	frame := lastFrame(output.String())
	if n := strings.Count(frame, "200"); n != 12 {
		t.Fatalf("got %d recent records, want 12", n)
	}
}

func lastFrame(output string) string {
	start := strings.LastIndex(output, "\x1b[H")
	if start < 0 {
		return output
	}
	_, frame, ok := strings.Cut(output[start:], "\x1b[2J")
	if !ok {
		return output
	}
	return frame
}
