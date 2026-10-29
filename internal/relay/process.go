package relay

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const shutdownGrace = 30 * time.Second

// Serve binds the validated loopback address and runs until cancellation.
// Shutdown drains for 30 seconds, then cancels and closes remaining streams.
func Serve(ctx context.Context, config *Config, logger *slog.Logger) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", config.listen)
	if err != nil {
		return errors.New("cannot bind loopback listener")
	}
	server := NewServer(config, logger)
	return server.serveListener(ctx, listener, shutdownGrace)
}

func (s *Server) serveListener(ctx context.Context, listener net.Listener, grace time.Duration) error {
	active, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	defer s.transport.CloseIdleConnections()
	server := &http.Server{
		Handler: s, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readBodyTimeout,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: maxHeaderBytes,
		BaseContext: func(net.Listener) context.Context { return active },
	}
	// The stdlib server can log raw connection/parser errors; use the same fixed
	// category sink as the proxy, never the default logger.
	server.ErrorLog = log.New(&serverLog{logger: s.logger}, "", 0)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	s.logger.Info("ready", "profile", s.config.profile, "listen", s.config.listen)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("listener failed")
	case <-ctx.Done():
	}
	drain, stop := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer stop()
	if err := server.Shutdown(drain); err != nil {
		cancel()
		_ = server.Close()
	}
	err := <-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("listener failed")
	}
	return nil
}

type serverLog struct{ logger *slog.Logger }

func (sink *serverLog) Write(value []byte) (int, error) {
	sink.logger.Error("HTTP server error", "category", "http_server_error")
	return len(value), nil
}
