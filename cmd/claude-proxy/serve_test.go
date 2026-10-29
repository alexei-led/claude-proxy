package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type readyHandler struct {
	slog.Handler
	ready chan struct{}
}

func (h readyHandler) Handle(ctx context.Context, record slog.Record) error {
	if err := h.Handler.Handle(ctx, record); err != nil {
		return err
	}
	if record.Message == "ready" {
		close(h.ready)
	}
	return nil
}

func TestRunServe(t *testing.T) {
	reserved, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err = reserved.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	if err = os.WriteFile(filepath.Join(dir, "access"), []byte("dummy-access"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Only the catalog is requested; startup must not contact this endpoint.
	body := fmt.Sprintf(`{"version":1,"profile":"personal","listen":%q,"access_token_file":"access","upstreams":{"local":{"messages_url":"http://127.0.0.1:1/messages","auth":{"mode":"none"}}},"models":{"native-id":{"upstream":"local"}}}`, address)
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ready := make(chan struct{})
	logger := slog.New(readyHandler{Handler: slog.NewJSONHandler(&output, nil), ready: ready})
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{configFlag, path}, logger) }()
	select {
	case <-ready:
	case err = <-done:
		t.Fatalf("startup failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no readiness")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Claude-Proxy-Key", "dummy-access")
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(data), "native-id") {
		t.Fatal("catalog unavailable")
	}
	stop()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if !strings.Contains(output.String(), `"msg":"ready"`) || strings.Contains(output.String(), "dummy-access") {
		t.Fatal("readiness/log boundary")
	}
}
