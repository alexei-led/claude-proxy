package managed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

type acceptStep struct {
	connection net.Conn
	err        error
}

type scriptedListener struct {
	steps []acceptStep
	calls int
}

func (listener *scriptedListener) Accept() (net.Conn, error) {
	step := listener.steps[listener.calls]
	listener.calls++
	return step.connection, step.err
}
func (*scriptedListener) Close() error   { return nil }
func (*scriptedListener) Addr() net.Addr { return &net.UnixAddr{Name: "fixture", Net: "unix"} }

func TestControlAcceptRetriesResourceErrorsAndReportsTerminalFailure(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	handled := make(chan struct{})
	listener := &scriptedListener{steps: []acceptStep{
		{err: &net.OpError{Op: "accept", Net: "unix", Err: syscall.EMFILE}},
		{err: syscall.EINTR},
		{connection: server},
		{err: errors.New("terminal listener failure")},
	}}
	err := serveControl(t.Context(), listener, func(connection net.Conn) { _ = connection.Close(); close(handled) })
	if err == nil || listener.calls != 4 {
		t.Fatal("control accept loop did not recover or report its terminal error")
	}
	<-handled
}

func TestControlAcceptCancellationIsNotAListenerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	listener := &scriptedListener{steps: []acceptStep{{err: syscall.EMFILE}}}
	if err := serveControl(ctx, listener, func(net.Conn) { t.Error("unexpected control handler") }); err != nil {
		t.Fatal(err)
	}
	if listener.calls != 1 {
		t.Fatal("canceled accept loop retried")
	}
}

func TestControlLoopWaitsForInFlightHandlers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer func() { _ = client.Close() }()
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan error, 1)
		listener := &scriptedListener{steps: []acceptStep{{connection: server}, {err: errors.New("terminal")}}}
		go func() {
			returned <- serveControl(t.Context(), listener, func(connection net.Conn) { close(entered); <-release; _ = connection.Close() })
		}()
		<-entered
		synctest.Wait()
		select {
		case err := <-returned:
			t.Fatalf("control loop returned before its handler exited: %v", err)
		default:
		}
		close(release)
		if err := <-returned; err == nil {
			t.Fatal("terminal accept failure was lost")
		}
	})
}

func TestBrokerObservesControlFailureAndClosesHTTPListener(t *testing.T) {
	_, config := fixture(t, "0")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	control := &scriptedListener{steps: []acceptStep{{err: errors.New("terminal accept failure")}}}
	server := relay.NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = serveBroker(t.Context(), config, server, control, listener, "test-generation")
	if err == nil || !strings.Contains(err.Error(), "managed control listener failed") {
		t.Fatalf("terminal control failure was not propagated: %v", err)
	}
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp4", listener.Addr().String())
	if err == nil {
		_ = connection.Close()
		t.Fatal("HTTP listener remained reachable after control failure")
	}
}
