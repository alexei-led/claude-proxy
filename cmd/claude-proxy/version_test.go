package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "claude-proxy")
	// #nosec G204 -- Build this package into a test-owned temporary directory.
	build := exec.CommandContext(t.Context(), "go", "build", "-ldflags=-X main.version=v9.8.7", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	for _, flag := range []string{"--version", "-version"} {
		// #nosec G204 -- Run only the test-built binary with fixed flags.
		output, err := exec.CommandContext(t.Context(), binary, flag).CombinedOutput()
		if err != nil || string(output) != "claude-proxy v9.8.7\n" {
			t.Fatalf("%s: output %q, error %v", flag, output, err)
		}
	}
	// #nosec G204 -- Run only the test-built binary with fixed arguments.
	output, err := exec.CommandContext(t.Context(), binary, "--version", "extra").CombinedOutput()
	if err == nil || strings.Contains(string(output), "claude-proxy v9.8.7") {
		t.Fatalf("unexpected success with extra argument: %q, %v", output, err)
	}
}
