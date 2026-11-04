// Command claude-proxy runs a foreground loopback-only native relay.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexei-led/claude-proxy/internal/console"
	"github.com/alexei-led/claude-proxy/internal/managed"
	"github.com/alexei-led/claude-proxy/internal/relay"
)

func main() {
	// The terminal is a live view, so it shows successful generations (DEBUG)
	// until the configuration says otherwise.
	level := new(slog.LevelVar)
	level.Set(slog.LevelDebug)
	logger := slog.New(console.New(os.Stderr, console.ColorEnabled(os.Stderr), level))
	if len(os.Args) > 1 && (os.Args[1] == commandManagedStart || os.Args[1] == commandManagedBroker) {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], logger, level)
	stop()
	if err != nil {
		message := "startup failed"
		if len(os.Args) > 1 && os.Args[1] == commandStatus {
			message = "status unavailable"
		}
		if len(os.Args) > 1 && os.Args[1] == commandStop {
			message = "stop failed"
		}
		logger.Error(message, "category", err.Error())
		os.Exit(1)
	}
}

// run executes one command. level is the foreground logger's minimum level;
// a configured runtime.log_level replaces it once the configuration loads.
func run(ctx context.Context, args []string, logger *slog.Logger, level *slog.LevelVar) error {
	if len(args) > 0 {
		switch args[0] {
		case "connect":
			return connect(ctx, args[1:])
		case "run":
			return managedRun(ctx, args[1:])
		case commandManagedStart, commandManagedBroker, commandStatus, commandStop:
			return managedCommand(ctx, args[0], args[1:])
		}
	}
	flags := flag.NewFlagSet("claude-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration file")
	check := flags.Bool("check", false, "validate configuration without listening")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		return errors.New("expected --config PATH [--check]")
	}
	config, err := relay.LoadConfig(*path)
	if err != nil {
		return err
	}
	level.Set(config.LogLevel(level.Level()))
	if *check {
		logger.Info("configuration valid", "profile", config.Profile())
		return nil
	}
	var activity func(int, bool, int)
	if handler, ok := logger.Handler().(*console.Handler); ok {
		handler.StartLive()
		defer handler.Close()
		activity = handler.Activity
	}
	return managed.ServeForeground(ctx, *path, config, logger, activity)
}
