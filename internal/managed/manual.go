package managed

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

// ServeForeground registers a manually owned server in the shared discovery
// registry. Existing matching servers are reported, never replaced or promoted.
func ServeForeground(ctx context.Context, path string, config *relay.Config, logger *slog.Logger) error {
	loc, err := locate(path, config)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lock, err := acquire(startup, loc.lock)
	if err != nil {
		return err
	}
	locked := true
	unlock := func() {
		if locked {
			_ = lock.Close()
			locked = false
		}
	}
	defer unlock()
	for {
		if info, err := exchange(startup, loc, request{Operation: operationStatus}); err == nil {
			logger.Info("already running", "profile", config.Profile(), "listen", info.Address, "pid", info.PID, "mode", info.Mode, "version", info.Version)
			return nil
		}
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(startup, "unix", loc.socket)
		if err != nil {
			break
		}
		_ = connection.Close()
		if err := pause(startup, 25*time.Millisecond); err != nil {
			return err
		}
	}
	if err := removeStaleSocket(loc.socket); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", config.Listen())
	if err != nil {
		return errors.New("cannot bind configured loopback listener; inspect the port or restart an older unregistered proxy")
	}
	defer func() { _ = listener.Close() }()
	control, err := listenControl(ctx, loc)
	if err != nil {
		return err
	}
	defer func() { _ = control.Close() }()
	logger.Info("starting", "profile", config.Profile(), "listen", listener.Addr().String(), "pid", os.Getpid(), "mode", ModeManual, "version", buildVersion())
	defer logger.Info("stopped", "profile", config.Profile(), "mode", ModeManual)
	return serveBroker(ctx, config, relay.NewServer(config, logger), control, listener, loc.identity, ModeManual, unlock)
}
