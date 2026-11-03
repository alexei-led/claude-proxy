package managed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

const (
	controlLimit = 64
	pollInterval = 250 * time.Millisecond
)

type lease struct {
	watch     *processWatch
	uncertain bool
}

type broker struct {
	mu        sync.Mutex
	leases    map[int]lease
	server    *relay.Server
	info      Info
	closing   bool
	cancel    context.CancelFunc
	idleSince time.Time
}

// StartDetached uses a short-lived starter so brokers are reaped by the OS,
// not left as unwaited children of Claude after exec.
func StartDetached(ctx context.Context, path, identity string) error {
	config, err := relay.LoadConfig(path)
	if err != nil {
		return err
	}
	loc, err := locate(path, config)
	if err != nil {
		return err
	}
	if loc.identity != identity {
		return errors.New("configuration or installed executable changed during startup")
	}
	file, err := openLog(loc.log)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	// #nosec G204 -- Only the current executable starts a broker; no shell or secrets in arguments.
	command := exec.CommandContext(context.WithoutCancel(ctx), loc.executable, "managed-broker", "--config", loc.config, "--identity", loc.identity)
	command.Env = brokerEnvironment()
	command.Stdout = file
	command.Stderr = file
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return errors.New("cannot detach managed proxy")
	}
	return command.Process.Release()
}

// Serve owns one detached broker until cancellation or verified idle shutdown.
func Serve(ctx context.Context, path, identity string) error {
	config, err := relay.LoadConfig(path)
	if err != nil {
		return err
	}
	loc, err := locate(path, config)
	if err != nil {
		return err
	}
	if identity != loc.identity {
		return errors.New("configuration or installed executable changed during startup")
	}
	logFile, err := openLog(loc.log)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	sink := &boundedLog{file: logFile}
	logger := slog.New(slog.NewJSONHandler(sink, nil))
	control, err := (&net.ListenConfig{}).Listen(ctx, "unix", loc.socket)
	if err != nil {
		return errors.New("cannot bind managed control socket")
	}
	// Only the startup-lock holder removes stale sockets. A retiring broker must
	// never unlink a socket that a replacement broker has just bound.
	control.(*net.UnixListener).SetUnlinkOnClose(false)
	defer func() { _ = control.Close() }()
	if err := os.Chmod(loc.socket, 0o600); err != nil {
		return errors.New("cannot secure managed control socket")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("cannot bind managed loopback listener")
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := relay.NewServer(config, logger)
	version := "development"
	if build, ok := debug.ReadBuildInfo(); ok && build.Main.Version != "" {
		version = build.Main.Version
	}
	state := &broker{cancel: cancel, leases: make(map[int]lease), server: server, info: Info{Address: listener.Addr().String(), Identity: identity, Version: version, PID: os.Getpid()}}
	controlsDone := make(chan error, 1)
	go func() {
		controlsDone <- serveControl(ctx, control, func(connection net.Conn) { state.handle(connection, identity) })
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, listener) }()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = control.Close()
			controlErr := <-controlsDone
			state.closeWatches()
			return errors.Join(controlErr, <-served)
		case err := <-served:
			cancel()
			_ = control.Close()
			controlErr := <-controlsDone
			state.closeWatches()
			return errors.Join(err, controlErr)
		case controlErr := <-controlsDone:
			canceled := ctx.Err() != nil
			cancel()
			_ = control.Close()
			serverErr := <-served
			state.closeWatches()
			if controlErr == nil && !canceled {
				controlErr = errors.New("managed control listener stopped unexpectedly")
			}
			return errors.Join(controlErr, serverErr)
		case now := <-ticker.C:
			if state.tick(now, config.IdleTimeout()) {
				cancel()
			}
		}
	}
}

func (b *broker) handle(connection net.Conn, identity string) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	var message request
	if err := json.NewDecoder(io.LimitReader(connection, 8192)).Decode(&message); err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	reply := response{}
	if message.Identity != identity || b.closing {
		reply.Error = "broker_not_available"
	} else {
		switch message.Operation {
		case operationStatus:
		case operationRegister:
			if message.PID < 1 {
				reply.Error = "invalid_client"
				break
			}
			watch, err := watchProcess(message.PID)
			if errors.Is(err, os.ErrProcessDone) {
				reply.Error = "client_exited"
				break
			}
			if old, exists := b.leases[message.PID]; exists && old.watch != nil {
				old.watch.close()
			}
			// Registration binds a fresh process identity, even if a numeric PID was reused.
			b.leases[message.PID] = lease{watch: watch}

			b.idleSince = time.Time{}
		case operationStop:
			if len(b.leases) > 0 || !b.server.BeginDrainIfIdle() {
				reply.Error = "broker_busy"
				break
			}
			b.closing = true
			defer b.cancel()
		default:
			reply.Error = "unknown_control_operation"
		}
	}
	reply.Info = b.snapshot()
	_ = json.NewEncoder(connection).Encode(reply)
}

func (b *broker) snapshot() Info {
	info := b.info
	info.Sessions = len(b.leases)
	info.Requests = b.server.ActiveRequests()
	for _, lease := range b.leases {
		if lease.watch == nil || lease.uncertain {
			info.Uncertain = true
		}
	}
	return info
}

func (b *broker) tick(now time.Time, idle time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for pid, lease := range b.leases {
		if lease.watch == nil {
			continue
		}
		alive, err := lease.watch.alive()
		if err != nil {
			lease.uncertain = true
			b.leases[pid] = lease
			continue
		}
		lease.uncertain = false
		b.leases[pid] = lease
		if !alive {
			lease.watch.close()
			delete(b.leases, pid)
		}
	}
	if idle == 0 || len(b.leases) > 0 || b.server.ActiveRequests() > 0 {
		b.idleSince = time.Time{}
		return false
	}
	if b.idleSince.IsZero() {
		b.idleSince = now
		return false
	}
	if now.Sub(b.idleSince) < idle {
		return false
	}
	if !b.server.BeginDrainIfIdle() {
		b.idleSince = time.Time{}
		return false
	}
	b.closing = true
	return true
}

func (b *broker) closeWatches() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, lease := range b.leases {
		if lease.watch != nil {
			lease.watch.close()
		}
	}
}

func serveControl(ctx context.Context, listener net.Listener, handle func(net.Conn)) error {
	var handlers sync.WaitGroup
	defer handlers.Wait()
	slots := make(chan struct{}, controlLimit)
	delay := 10 * time.Millisecond
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if recoverableAccept(err) {
				if err := pause(ctx, delay); err != nil {
					return nil
				}
				delay = min(delay*2, time.Second)
				continue
			}
			return errors.New("managed control listener failed")
		}
		delay = 10 * time.Millisecond
		select {
		case slots <- struct{}{}:
		default:
			_ = connection.Close()
			continue
		}
		handlers.Go(func() { defer func() { <-slots }(); handle(connection) })
	}
}

func recoverableAccept(err error) bool {
	for _, code := range []error{syscall.EINTR, syscall.EAGAIN, syscall.ECONNABORTED, syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}
