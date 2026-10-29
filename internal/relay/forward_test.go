package relay

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type capture struct {
	body   string
	query  string
	path   string
	host   string
	header http.Header
}

func configureUpstream(t *testing.T, config *Config, address, mode string, count bool) {
	t.Helper()
	target, err := url.Parse(address + "/explicit/messages")
	if err != nil {
		t.Fatal(err)
	}
	route := &upstream{messages: target, mode: mode, key: testServerKey}
	if count {
		countURL := *target
		countURL.Path = "/separate/count"
		route.count = &countURL
	}
	config.models[testModel] = model{upstream: route, label: "native"}
}

type responseSnapshot struct {
	StatusCode int
	Header     http.Header
	Trailer    http.Header
}

func roundTrip(t *testing.T, client *http.Client, req *http.Request) (responseSnapshot, []byte) {
	t.Helper()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return responseSnapshot{StatusCode: response.StatusCode, Header: response.Header.Clone(), Trailer: response.Trailer.Clone()}, body
}

func networkRequest(t *testing.T, address, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testHost
	req.Header.Set(localKeyHeader, testToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer dummy-oauth")
	return req
}

func TestForward(t *testing.T) {
	captures := make(chan capture, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		captures <- capture{body: string(body), query: r.URL.RawQuery, path: r.URL.Path, host: r.Host, header: r.Header.Clone()}
		w.Header().Set("Request-Id", "dummy-request")
		w.Header().Set("Retry-After", "12")
		w.Header().Set("Set-Cookie", "secret=bad")
		w.Header().Set(localKeyHeader, "dummy-leak")
		w.Header().Set("Trailer", "X-Api-Key, X-Result")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("raw upstream failure\n"))
		w.Header().Set("X-Api-Key", "dummy-trailer-leak")
		w.Header().Set("X-Result", "done")
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authOAuth, true)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	fixture, err := os.ReadFile("testdata/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	body := " \n" + string(fixture) + " \n"
	for _, path := range []string{messagesPath, countTokensPath} {
		req := networkRequest(t, local.URL, path+"?beta=true&trace=a;b&x=%2f&x=1", body)
		req.Header.Set("Anthropic-Beta", "oauth-beta")
		req.Header.Set("X-Organization", "dummy-org")
		req.Header.Set("X-Api-Key", "dummy-conflicting-key")
		req.Header.Set("Connection", "Authorization, X-Claude-Proxy-Key")
		response, out := roundTrip(t, local.Client(), req)
		if response.StatusCode != http.StatusTooManyRequests || string(out) != "raw upstream failure\n" {
			t.Fatal("upstream failure changed")
		}
		if response.Header.Get("Request-Id") != "dummy-request" || response.Header.Get("Retry-After") != "12" {
			t.Fatal("response header lost")
		}
		if response.Header.Get("Set-Cookie") != "" || response.Header.Get(localKeyHeader) != "" || response.Trailer.Get("X-Api-Key") != "" {
			t.Fatal("response credential leaked")
		}
		if response.Trailer.Get("X-Result") != "done" {
			t.Fatal("safe trailer lost")
		}
		got := <-captures
		wantPath := "/explicit/messages"
		if path == countTokensPath {
			wantPath = "/separate/count"
		}
		if got.body != body || got.query != "beta=true&trace=a;b&x=%2f&x=1" || got.path != wantPath || got.host != strings.TrimPrefix(upstream.URL, "http://") {
			t.Fatalf("request changed: %+v", got)
		}
		if got.header.Get("Authorization") != "Bearer dummy-oauth" || got.header.Get("X-Organization") != "dummy-org" || got.header.Get("Anthropic-Beta") != "oauth-beta" {
			t.Fatal("OAuth context lost")
		}
		if got.header.Get(localKeyHeader) != "" || got.header.Get("X-Api-Key") != "" {
			t.Fatal("conflicting credential leaked")
		}
	}
}

func TestCredentials(t *testing.T) {
	for _, mode := range []string{authBearer, authAPIKey, authNone} {
		t.Run(mode, func(t *testing.T) {
			captured := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured <- r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			config := testConfig(t)
			configureUpstream(t, config, upstream.URL, mode, false)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			req := networkRequest(t, local.URL, messagesPath, `{"model":"exact-model"}`)
			req.Header.Set("X-Device-Id", "dummy-device")
			req.Header.Set("X-Organization", "dummy-org")
			req.Header.Set("X-Api-Key", "dummy-client")
			req.Header.Set("Connection", "Authorization, X-Api-Key")
			_, _ = roundTrip(t, local.Client(), req)
			got := <-captured
			wantAuth, wantKey := "", ""
			if mode == authBearer {
				wantAuth = "Bearer dummy-server"
			}
			if mode == authAPIKey {
				wantKey = testServerKey
			}
			if got.Get("Authorization") != wantAuth || got.Get("X-Api-Key") != wantKey {
				t.Fatal("configured credential removed or replaced")
			}
			for _, key := range []string{localKeyHeader, "X-Device-Id", "X-Organization"} {
				if got.Get(key) != "" {
					t.Fatalf("leaked %s", key)
				}
			}
		})
	}
}

func TestUpstreamFailure(t *testing.T) {
	for _, status := range []int{302, 400, 401, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "opaque failure")
			}))
			defer upstream.Close()
			config := testConfig(t)
			configureUpstream(t, config, upstream.URL, authNone, false)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			client := local.Client()
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
			response, body := roundTrip(t, client, networkRequest(t, local.URL, messagesPath, `{"model":"exact-model"}`))
			if response.StatusCode != status || string(body) != "opaque failure" || calls.Load() != 1 {
				t.Fatal("failure changed, redirected or replayed")
			}
		})
	}
}

func TestForwardCompression(t *testing.T) {
	compressed := []byte{0x1f, 0x8b, 0x08, 0xff, 0x00}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	client := local.Client()
	client.Transport = &http.Transport{DisableCompression: true}
	defer client.CloseIdleConnections()
	response, body := roundTrip(t, client, networkRequest(t, local.URL, messagesPath, `{"model":"exact-model"}`))
	if response.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(body, compressed) {
		t.Fatal("compressed bytes changed")
	}
}

func TestStreaming(t *testing.T) {
	const first = ": ping\n\nevent: unfamiliar\ndata: {\"x\":1}\n\n"
	const last = "event: message_stop\ndata: {}\n\n"
	release := make(chan struct{})
	canceled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-release:
			_, _ = io.WriteString(w, last)
		case <-r.Context().Done():
			canceled <- struct{}{}
		}
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authOAuth, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	req := networkRequest(t, local.URL, messagesPath, `{"model":"exact-model","stream":true}`)
	client := local.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	chunk := make([]byte, len(first))
	if _, err = io.ReadFull(response.Body, chunk); err != nil {
		close(release)
		t.Fatal(err)
	}
	if string(chunk) != first {
		close(release)
		t.Fatal("first chunk changed")
	}
	if err = response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("upstream not canceled")
	}
	close(release)
}
