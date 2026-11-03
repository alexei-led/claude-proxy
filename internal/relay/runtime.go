package relay

import (
	"context"
	"errors"
	"net"
	"time"
)

const (
	defaultHeaderWait = 180 * time.Second
	maxHeaderWait     = 10 * time.Minute
)

type runtimeValues struct {
	idle       time.Duration
	limit      int
	headerWait time.Duration
}

func runtimeSettings(raw runtimeConfig) (runtimeValues, error) {
	values := runtimeValues{idle: 5 * time.Minute, limit: maxInflight, headerWait: defaultHeaderWait}
	if raw.IdleTimeout != "" {
		var err error
		values.idle, err = time.ParseDuration(raw.IdleTimeout)
		if err != nil || values.idle < 0 {
			return runtimeValues{}, errors.New("invalid managed idle timeout")
		}
	}
	if raw.MaxInflight != nil {
		values.limit = *raw.MaxInflight
		if values.limit < 1 || values.limit > 64 {
			return runtimeValues{}, errors.New("runtime concurrency must be between 1 and 64")
		}
	}
	if raw.ResponseHeaderTimeout != "" {
		var err error
		values.headerWait, err = time.ParseDuration(raw.ResponseHeaderTimeout)
		if err != nil || values.headerWait < time.Second || values.headerWait > maxHeaderWait {
			return runtimeValues{}, errors.New("response header timeout must be between 1s and 10m")
		}
	}
	return values, nil
}

// ResponseHeaderWait returns how long one upstream attempt may take to answer with headers.
func (c *Config) ResponseHeaderWait() time.Duration {
	if c.headerWait == 0 {
		return defaultHeaderWait
	}
	return c.headerWait
}

// IdleTimeout returns the managed broker's zero-session timeout.
func (c *Config) IdleTimeout() time.Duration { return c.idleTimeout }

// MaxInflight returns the bounded forwarding concurrency.
func (c *Config) MaxInflight() int {
	if c.concurrency == 0 {
		return maxInflight
	}
	return c.concurrency
}

// Fingerprint identifies the immutable configuration and credential snapshot.
// It is private runtime identity, not public diagnostics.
func (c *Config) Fingerprint() [32]byte { return c.fingerprint }

// ClientAt projects only the local client credentials for a bound address.
func (c *Config) ClientAt(address string) *ClientConfig {
	models := make([]string, 0, len(c.models))
	for id := range c.models {
		models = append(models, id)
	}
	return &ClientConfig{BaseURL: "http://" + address, LocalKey: c.token, Models: models}
}

// ActiveRequests reports forwarded requests, including live streams.
func (s *Server) ActiveRequests() int {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return len(s.slots)
}

// BeginDrainIfIdle prevents new forwarding admission only when no request is active.
func (s *Server) BeginDrainIfIdle() bool {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if len(s.slots) != 0 {
		return false
	}
	s.draining = true
	return true
}

// Serve runs on an already-bound literal loopback listener.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) || address.Port == 0 {
		return errors.New("listener must be a bound literal loopback address")
	}
	snapshot := *s.config
	snapshot.listen = listener.Addr().String()
	s.config = &snapshot
	return s.serveListener(ctx, listener, shutdownGrace)
}
