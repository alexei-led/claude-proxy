package managed

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
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
