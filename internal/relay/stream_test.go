package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamingComplete(t *testing.T) {
	const stream = ": ping\n\nevent: unfamiliar\ndata: {}\n\nevent: message_stop\ndata: {}\n\n"
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, stream)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	client := local.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Do(networkRequest(t, local.URL, messagesPath, testBody))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	close(release)
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != stream {
		t.Fatalf("body changed, err=%v", err)
	}
}

func TestStreamingTruncatedNoReplay(t *testing.T) {
	const partial = "event: content_block_delta\ndata: {}\n\n"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, partial)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		connection, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authOAuth, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	req := networkRequest(t, local.URL, messagesPath, testBody)
	req.Header.Set("Idempotency-Key", "dummy-idempotency")
	response, err := local.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	if readErr == nil || string(body) != partial {
		t.Fatalf("truncation hidden or bytes changed: %q, %v", body, readErr)
	}
	if calls.Load() != 1 {
		t.Fatal("accepted generation replayed")
	}
}

func TestUpstreamFailureNoHeaders(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(map[bool]string{false: "connection", true: "TLS"}[encrypted], func(t *testing.T) {
			var calls atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				connection, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
			})
			upstream := httptest.NewUnstartedServer(handler)
			if encrypted {
				upstream.StartTLS()
			} else {
				upstream.Start()
			}
			defer upstream.Close()
			config := testConfig(t)
			configureUpstream(t, config, upstream.URL, authNone, false)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			req := networkRequest(t, local.URL, messagesPath, testBody)
			req.Header.Set("Idempotency-Key", "dummy-idempotency")
			response, _ := roundTrip(t, local.Client(), req)
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d", response.StatusCode)
			}
			wantCalls := int32(1)
			if encrypted {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatal("TLS bypass or request replay")
			}
		})
	}
}

func TestForwardReusedConnectionNoReplay(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if call == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		connection, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	for _, want := range []int{http.StatusOK, http.StatusBadGateway} {
		req := networkRequest(t, local.URL, messagesPath, testBody)
		req.Header.Set("Idempotency-Key", "dummy-idempotency")
		response, _ := roundTrip(t, local.Client(), req)
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d", response.StatusCode, want)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("request replayed")
	}
}

func TestStreamingCancellationReleasesSlot(t *testing.T) {
	entered := make(chan struct{}, 1)
	exited := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
		exited <- struct{}{}
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	req := networkRequest(t, local.URL, messagesPath, testBody)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := local.Client().Do(req)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not entered")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not canceled")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish")
	}
	for range maxInflight {
		select {
		case relay.slots <- struct{}{}:
		case <-time.After(3 * time.Second):
			t.Fatal("slot leaked")
		}
	}
	for range maxInflight {
		<-relay.slots
	}
}
