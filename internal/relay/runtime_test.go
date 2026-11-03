package relay

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		valid          bool
		limit          int
		idle           time.Duration
	}{
		{"defaults", "", true, 8, 5 * time.Minute},
		{"configured", `"runtime":{"idle_timeout":"2m","max_inflight":32},`, true, 32, 2 * time.Minute},
		{"disabled idle", `"runtime":{"idle_timeout":"0"},`, true, 8, 0},
		{"unknown field", `"runtime":{"idle":1},`, false, 0, 0},
		{"negative idle", `"runtime":{"idle_timeout":"-1s"},`, false, 0, 0},
		{"bad duration", `"runtime":{"idle_timeout":"later"},`, false, 0, 0},
		{"zero concurrency", `"runtime":{"max_inflight":0},`, false, 0, 0},
		{"excess concurrency", `"runtime":{"max_inflight":65},`, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := configFixture(t, func(body string) string {
				return strings.Replace(body, versionFragment, tc.settings+versionFragment, 1)
			})
			config, err := LoadConfig(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			if config.IdleTimeout() != tc.idle {
				t.Fatal("managed idle timeout not applied")
			}
			server := NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
			defer server.transport.CloseIdleConnections()
			if cap(server.slots) != tc.limit || server.transport.MaxConnsPerHost != tc.limit {
				t.Fatal("runtime concurrency not applied")
			}
		})
	}
}

func TestSelectedCredentialRotationCreatesNewSnapshotIdentity(t *testing.T) {
	path := configFixture(t, func(body string) string {
		return strings.Replace(body, oauthFragment, `"mode":"bearer","key_file":"provider-key"`, 1)
	})
	key := filepath.Join(filepath.Dir(path), "provider-key")
	if err := os.WriteFile(key, []byte("first-dummy-provider-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("other-dummy-provider-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == next.Fingerprint() {
		t.Fatal("provider-key rotation reused a generation")
	}
	if first.models[testModel].upstream.key != "first-dummy-provider-key" {
		t.Fatal("credential rotation mutated an existing snapshot")
	}
}
