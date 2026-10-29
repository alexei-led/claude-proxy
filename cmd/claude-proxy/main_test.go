package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const configFlag = "--config"

func TestCheck(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "access")
	if err := os.WriteFile(keyPath, []byte("dummy-local-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	config := fmt.Sprintf(
		`{"version":1,"profile":"personal","listen":%q,"access_token_file":"access","upstreams":{"native":{"messages_url":%q,"auth":{"mode":"oauth_passthrough"}}},"models":{"exact-model":{"upstream":"native"}}}`, occupied.Addr().String(), upstream.URL+"/messages")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	if err := run(t.Context(), []string{configFlag, path, "--check"}, logger); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("check contacted upstream")
	}
	if !strings.Contains(output.String(), "configuration valid") {
		t.Fatal("missing validation record")
	}
	for _, secret := range []string{"dummy-local-secret", upstream.URL, dir, "exact-model"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("sensitive output")
		}
	}
	for _, args := range [][]string{nil, {"--unknown=dummy-secret"}, {configFlag, path, "extra"}, {configFlag, "missing"}} {
		err := run(context.Background(), args, logger)
		if err == nil || strings.Contains(err.Error(), "dummy-secret") {
			t.Fatal("invalid argument handling")
		}
	}
}
