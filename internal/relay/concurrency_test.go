package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfiguredThirtyTwoConcurrentRequests(t *testing.T) {
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) < 1<<20 {
			t.Error("request body was not forwarded intact")
		}
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	path := configFixture(t, func(body string) string {
		body = strings.Replace(body, versionFragment, `"runtime":{"max_inflight":32},`+versionFragment, 1)
		return strings.ReplaceAll(body, "http://127.0.0.1:12345/native/messages", upstream.URL+"/messages")
	})
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer server.transport.CloseIdleConnections()
	payload, err := json.Marshal(map[string]string{"model": testModel, "padding": strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	call := func() int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, messagesPath, bytes.NewReader(payload))
		req.Host = config.listen
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(localKeyHeader, config.token)
		req.Header.Set("Authorization", "Bearer dummy-oauth")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		return response.Code
	}
	done := make(chan int, 32)
	for range 32 {
		go func() { done <- call() }()
	}
	for range 32 {
		<-entered
	}
	if peak.Load() != 32 || server.ActiveRequests() != 32 {
		t.Fatal("configured concurrency was not admitted")
	}
	if code := call(); code != http.StatusTooManyRequests {
		t.Fatalf("overflow status=%d", code)
	}
	if server.BeginDrainIfIdle() {
		t.Fatal("idle shutdown admitted while requests were active")
	}
	close(release)
	for range 32 {
		if code := <-done; code != http.StatusOK {
			t.Fatalf("forward status=%d", code)
		}
	}
	if !server.BeginDrainIfIdle() {
		t.Fatal("idle shutdown was unavailable after completion")
	}
	if code := call(); code != http.StatusServiceUnavailable {
		t.Fatalf("draining status=%d", code)
	}
}
