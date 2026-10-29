package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBody = `{"model":"exact-model"}`

func TestHTTPBoundaryBodyLimit(t *testing.T) {
	relay := newTestRelay(t, testConfig(t))
	defer relay.transport.CloseIdleConnections()
	for _, declared := range []int64{-1, maxBodyBytes + 1} {
		req := request(http.MethodPost, messagesPath, "")
		req.Body = io.NopCloser(io.LimitReader(repeatReader{}, maxBodyBytes+1))
		req.ContentLength = declared
		out := httptest.NewRecorder()
		relay.ServeHTTP(out, req)
		if out.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d", out.Code)
		}
	}
}

type repeatReader struct{}

func (repeatReader) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = ' '
	}
	return len(buffer), nil
}

func TestHTTPBoundaryHeaderLimit(t *testing.T) {
	config := testConfig(t)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewUnstartedServer(relay)
	local.Config.MaxHeaderBytes = maxHeaderBytes
	local.Config.ReadHeaderTimeout = readHeaderTimeout
	local.Start()
	defer local.Close()
	req := networkRequest(t, local.URL, messagesPath, testBody)
	req.Header.Set("X-Large", strings.Repeat("x", maxHeaderBytes+8192))
	response, _ := roundTrip(t, local.Client(), req)
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestHTTPBoundaryOverload(t *testing.T) {
	entered := make(chan struct{}, maxInflight)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	defer unblock()
	done := make(chan int, maxInflight)
	for range maxInflight {
		req := networkRequest(t, local.URL, messagesPath, testBody)
		go func() {
			response, err := local.Client().Do(req)
			if err != nil {
				done <- 0
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			done <- response.StatusCode
		}()
	}
	for range maxInflight {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("requests not admitted")
		}
	}
	response, _ := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d", response.StatusCode)
	}
	catalogReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+modelsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalogReq.Host = testHost
	catalogReq.Header.Set(localKeyHeader, testToken)
	response, _ = roundTrip(t, local.Client(), catalogReq)
	if response.StatusCode != http.StatusOK {
		t.Fatal("catalog blocked by generation slots")
	}
	unblock()
	for range maxInflight {
		select {
		case status := <-done:
			if status != http.StatusOK {
				t.Fatalf("status=%d", status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("forwarded request stuck")
		}
	}
	response, _ = roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	if response.StatusCode != http.StatusOK {
		t.Fatal("slot not released")
	}
}

func TestShutdown(t *testing.T) {
	canceled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": ping\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- relay.serveListener(ctx, listener, 20*time.Millisecond) }()
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	req := networkRequest(t, "http://"+listener.Addr().String(), messagesPath, testBody)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	chunk := make([]byte, 8)
	if _, err = io.ReadFull(response.Body, chunk); err != nil {
		t.Fatal(err)
	}
	stop()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown exceeded bound")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel upstream")
	}
	if _, err = io.ReadAll(response.Body); err == nil {
		t.Fatal("forced stream shutdown appeared complete")
	}
}

func TestForwardResponseHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	relay.transport.ResponseHeaderTimeout = 20 * time.Millisecond
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	response, _ := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status=%s", strconv.Itoa(response.StatusCode))
	}
}
