package console

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	idleDisplayAfter = 30 * time.Second
	recentRecords    = 12
)

type liveView struct {
	heading      string
	finalLine    string
	lines        []string
	lastActivity time.Time
	registered   int
	requests     int
	uncertain    bool
	initialized  bool
}

// StartLive uses the alternate screen only on a capable terminal. NO_COLOR
// controls colors separately; redirected output always remains append-only.
func (handler *Handler) StartLive() {
	file, ok := handler.writer.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" || !isTerminal(file) {
		return
	}
	handler.startLive()
}

func (handler *Handler) startLive() {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	handler.view = &liveView{}
	_, _ = io.WriteString(handler.writer, "\x1b[?1049h")
}

// Close restores the original terminal screen after a live foreground run.
func (handler *Handler) Close() {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if handler.view != nil {
		_, _ = io.WriteString(handler.writer, "\x1b[?1049l"+handler.view.finalLine)
		handler.view = nil
	}
}

// Activity refreshes current registered sessions and forwarding requests without
// emitting log records. The broker owns its polling and shutdown.
func (handler *Handler) Activity(registered int, uncertain bool, requests int) {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	view := handler.view
	if view == nil {
		return
	}
	changed := !view.initialized || registered != view.registered || uncertain != view.uncertain || requests != view.requests
	view.registered, view.uncertain, view.requests = registered, uncertain, requests
	view.initialized = true
	if requests > 0 {
		view.lastActivity = time.Now()
	}
	if requests == 0 && !view.lastActivity.IsZero() && time.Since(view.lastActivity) >= idleDisplayAfter {
		view.lines = nil
		view.lastActivity = time.Time{}
		changed = true
	}
	if changed {
		_ = handler.renderLive()
	}
}

func (handler *Handler) liveRecord(message, line string, now time.Time) error {
	view := handler.view
	switch message {
	case messageStarting, messageReady:
		view.heading = line
	case "stopped", "already running":
		view.finalLine = line
	case "stats":
		if view.lastActivity.IsZero() && view.requests == 0 {
			return nil
		}
	default:
		view.lastActivity = now
	}
	if message != messageStarting && message != messageReady {
		view.lines = append(view.lines, line)
		if len(view.lines) > recentRecords {
			view.lines = view.lines[len(view.lines)-recentRecords:]
		}
	}
	return handler.renderLive()
}

func (handler *Handler) renderLive() error {
	view := handler.view
	state := "IDLE"
	if view.requests > 0 {
		state = "ACTIVE"
	} else if !view.lastActivity.IsZero() {
		state = "RECENT"
	}
	tracking := ""
	if view.uncertain {
		tracking = " · tracking uncertain"
	}
	status := fmt.Sprintf("%s · %d registered sessions · %d active requests%s · manual clients not counted\n",
		state, view.registered, view.requests, tracking)
	_, err := io.WriteString(handler.writer, "\x1b[H\x1b[2J"+view.heading+status+"\n"+strings.Join(view.lines, ""))
	return err
}
