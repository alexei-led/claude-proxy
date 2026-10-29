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

	"github.com/alexei-led/claude-proxy/internal/relay"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], logger)
	stop()
	if err != nil {
		logger.Error("startup failed", "category", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, logger *slog.Logger) error {
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
	if *check {
		logger.Info("configuration valid", "profile", config.Profile())
		return nil
	}
	return relay.Serve(ctx, config, logger)
}
