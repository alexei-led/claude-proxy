package managed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

const (
	operationRegister = "register"
	operationStatus   = "status"
	operationStop     = "stop"
)

// Info reports one current-generation broker without credentials.
type Info struct {
	Address   string `json:"address"`
	Identity  string `json:"identity"`
	Version   string `json:"version"`
	PID       int    `json:"pid"`
	Sessions  int    `json:"sessions"`
	Requests  int    `json:"requests"`
	Uncertain bool   `json:"tracking_uncertain"`
}

type request struct {
	Operation string `json:"operation"`
	Identity  string `json:"identity"`
	PID       int    `json:"pid"`
}

type response struct {
	Info
	Error string `json:"error,omitempty"`
}

// Ensure starts or reuses a matching broker and registers this process before exec.
func Ensure(ctx context.Context, path string, config *relay.Config) (Info, error) {
	loc, err := locate(path, config)
	if err != nil {
		return Info{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lock, err := acquire(ctx, loc.lock)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = lock.Close() }()
	for {
		if info, err := exchange(ctx, loc, request{Operation: operationRegister, PID: os.Getpid()}); err == nil {
			return info, nil
		}
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "unix", loc.socket)
		if err != nil {
			break
		}
		_ = connection.Close()
		if err := pause(ctx, 25*time.Millisecond); err != nil {
			return Info{}, err
		}
	}
	if err := removeStaleSocket(loc.socket); err != nil {
		return Info{}, err
	}
	// #nosec G204 -- Start only this installed binary; arguments contain no secrets.
	command := exec.CommandContext(ctx, loc.executable, "managed-start", "--config", loc.config, "--identity", loc.identity)
	command.Env = brokerEnvironment()
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return Info{}, errors.New("cannot start managed proxy")
	}
	return waitForBroker(ctx, loc)
}

func waitForBroker(ctx context.Context, loc location) (Info, error) {
	for {
		info, err := exchange(ctx, loc, request{Operation: operationRegister, PID: os.Getpid()})
		if err == nil {
			return info, nil
		}
		if err := pause(ctx, 25*time.Millisecond); err != nil {
			return Info{}, errors.New("managed proxy did not become ready; inspect its private runtime log")
		}
	}
}

func exchange(ctx context.Context, loc location, message request) (Info, error) {
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "unix", loc.socket)
	if err != nil {
		return Info{}, errors.New("managed proxy is not running")
	}
	defer func() { _ = connection.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = connection.SetDeadline(deadline)
	message.Identity = loc.identity
	if err := json.NewEncoder(connection).Encode(message); err != nil {
		return Info{}, errors.New("managed proxy control request failed")
	}
	var reply response
	if err := json.NewDecoder(io.LimitReader(connection, 8192)).Decode(&reply); err != nil {
		return Info{}, errors.New("invalid managed proxy control response")
	}
	if reply.Error != "" || reply.Identity != loc.identity {
		return Info{}, errors.New("managed proxy rejected the control request")
	}
	host, port, err := net.SplitHostPort(reply.Address)
	number, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || host != "127.0.0.1" || number < 1 || number > 65535 {
		return Info{}, errors.New("invalid managed proxy address")
	}
	return reply.Info, nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("unsafe stale broker socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return errors.New("broker socket has a different owner")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("cannot remove stale broker socket")
	}
	return nil
}

func brokerEnvironment() []string {
	result := make([]string, 0, 5)
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "TZ"} {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	return result
}

// Status returns the current generation without starting a process.
func Status(ctx context.Context, path string, config *relay.Config) (Info, error) {
	loc, err := locate(path, config)
	if err != nil {
		return Info{}, err
	}
	return exchange(ctx, loc, request{Operation: operationStatus})
}

// Stop closes an idle current-generation broker; active clients are never interrupted.
func Stop(ctx context.Context, path string, config *relay.Config) (Info, error) {
	loc, err := locate(path, config)
	if err != nil {
		return Info{}, err
	}
	return exchange(ctx, loc, request{Operation: operationStop})
}
