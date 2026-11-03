package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/alexei-led/claude-proxy/internal/managed"
	"github.com/alexei-led/claude-proxy/internal/relay"
)

const (
	commandStatus        = "status"
	commandStop          = "stop"
	commandManagedStart  = "managed-start"
	commandManagedBroker = "managed-broker"
)

func managedRun(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration file")
	if err := flags.Parse(args); err != nil || *path == "" || flags.NArg() == 0 {
		return errors.New("expected run --config PATH -- COMMAND [ARGS]")
	}
	config, err := relay.LoadConfig(*path)
	if err != nil {
		return err
	}
	command := flags.Args()
	if err := checkSettingsArguments(command); err != nil {
		return err
	}
	if _, err := clientEnvironment(os.Environ(), config.ClientAt(config.Listen())); err != nil {
		return err
	}
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return errors.New("client executable is unavailable")
	}
	info, err := managed.Ensure(ctx, *path, config)
	if err != nil {
		return err
	}
	if info.Uncertain {
		_, _ = fmt.Fprintln(os.Stderr, "Managed proxy: process tracking is uncertain; automatic shutdown is disabled while it remains uncertain.")
	}
	client := config.ClientAt(info.Address)
	environment, err := clientEnvironment(os.Environ(), client)
	if err != nil {
		return err
	}
	if err := checkCatalog(ctx, client); err != nil {
		return err
	}
	// #nosec G204 -- The explicit operator-selected command replaces this registered PID.
	if err := syscall.Exec(executable, command, environment); err != nil {
		return errors.New("client execution failed")
	}
	return nil
}

func managedCommand(ctx context.Context, operation string, args []string) error {
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration file")
	identity := flags.String("identity", "", "private generation identity")
	if err := flags.Parse(args); err != nil || *path == "" || flags.NArg() != 0 {
		return errors.New("invalid managed proxy arguments")
	}
	switch operation {
	case commandManagedStart:
		if *identity == "" {
			return errors.New("managed generation identity is required")
		}
		return managed.StartDetached(ctx, *path, *identity)
	case commandManagedBroker:
		if *identity == "" {
			return errors.New("managed generation identity is required")
		}
		return managed.Serve(ctx, *path, *identity)
	case commandStatus, commandStop:
		config, err := relay.LoadConfig(*path)
		if err != nil {
			return err
		}
		var info managed.Info
		if operation == commandStop {
			info, err = managed.Stop(ctx, *path, config)
		} else {
			info, err = managed.Status(ctx, *path, config)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(info)
	default:
		return errors.New("unknown managed proxy operation")
	}
}
