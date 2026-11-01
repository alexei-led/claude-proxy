package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConnectPreflightWithoutProviderKeys(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("X-Claude-Proxy-Key") != "dummy-local-access" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected preflight request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"native-model"}],"has_more":false}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "access"), []byte("dummy-local-access"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profile.json")
	body := fmt.Sprintf(`{"version":1,"profile":"work","listen":%q,"access_token_file":"access","upstreams":{"native":{"messages_url":"https://example.invalid/v1/messages","auth":{"mode":"bearer","key_file":"provider-key-not-available-to-client"}}},"models":{"native-model":{"upstream":"native"}}}`, strings.TrimPrefix(server.URL, "http://"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	err := run(t.Context(), []string{"connect", configFlag, path, "--", filepath.Join(dir, "missing-client")}, logger)
	if err == nil || err.Error() != "client executable is unavailable" {
		t.Fatalf("expected client lookup after successful preflight, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("catalog calls = %d", calls.Load())
	}
}
