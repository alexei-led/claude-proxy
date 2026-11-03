package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	statusOK        = http.StatusOK
	statusBusy      = http.StatusServiceUnavailable
	replayedPayload = `{"model":"m"}`
)

// outcome is one scripted upstream answer: a status, or a transport error.
type outcome struct {
	status     int
	retryAfter string
	err        error
}

// script plays one outcome per call and records every body the "upstream" received.
type script struct {
	mu       sync.Mutex
	outcomes []outcome
	bodies   []string
}

func (s *script) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(body))
	next := s.outcomes[min(len(s.bodies), len(s.outcomes))-1]
	if next.err != nil {
		return nil, next.err
	}
	header := http.Header{}
	if next.retryAfter != "" {
		header.Set("Retry-After", next.retryAfter)
	}
	return &http.Response{StatusCode: next.status, Header: header, Body: io.NopCloser(strings.NewReader("body"))}, nil
}

type attemptResult struct {
	status  int // 0 when the round trip failed
	err     error
	bodies  []string
	retries int
}

func runRetrier(t *testing.T, attempts int, outcomes ...outcome) attemptResult {
	t.Helper()
	upstream := &script{outcomes: outcomes}
	state := &replay{body: []byte(replayedPayload)}
	ctx := context.WithValue(t.Context(), replayKey{}, state)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/v1/messages", io.NopCloser(bytes.NewReader(state.body)))
	if err != nil {
		t.Fatal(err)
	}
	result := attemptResult{}
	resp, err := (&retrier{base: upstream, attempts: attempts, delay: time.Millisecond}).RoundTrip(req)
	if resp != nil {
		result.status = resp.StatusCode
		_ = resp.Body.Close()
	}
	result.err, result.bodies, result.retries = err, upstream.bodies, state.retries
	return result
}

func TestRetrierOutcomes(t *testing.T) {
	dial := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	reset := &net.OpError{Op: "read", Err: errors.New("connection reset")}
	busy, overloaded, ok := outcome{status: statusBusy}, outcome{status: statusOverloaded}, outcome{status: statusOK}
	cases := []struct {
		name        string
		attempts    int
		outcomes    []outcome
		wantStatus  int
		wantErr     bool
		wantCalls   int
		wantRetries int
	}{
		{"503 then success", 3, []outcome{busy, ok}, statusOK, false, 2, 1},
		{"529 then success", 3, []outcome{overloaded, ok}, statusOK, false, 2, 1},
		{"two failures then success", 3, []outcome{busy, busy, ok}, statusOK, false, 3, 2},
		{"exhausted returns the last upstream answer", 3, []outcome{busy}, statusBusy, false, 3, 2},
		{"500 is not retried", 3, []outcome{{status: 500}, ok}, 500, false, 1, 0},
		{"429 is not retried", 3, []outcome{{status: 429}, ok}, 429, false, 1, 0},
		{"200 is not retried", 3, []outcome{ok}, statusOK, false, 1, 0},
		{"short Retry-After is honored", 3, []outcome{{status: statusBusy, retryAfter: "0"}, ok}, statusOK, false, 2, 1},
		{"long Retry-After is passed through", 3, []outcome{{status: statusBusy, retryAfter: "60"}, ok}, statusBusy, false, 1, 0},
		{"future date Retry-After is passed through", 3, []outcome{{status: statusBusy, retryAfter: time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)}, ok}, statusBusy, false, 1, 0},
		{"past date Retry-After is retried", 3, []outcome{{status: statusBusy, retryAfter: "Wed, 21 Oct 2015 07:28:00 GMT"}, ok}, statusOK, false, 2, 1},
		{"overflowing Retry-After is passed through", 3, []outcome{{status: statusBusy, retryAfter: "10000000000"}, ok}, statusBusy, false, 1, 0},
		{"garbage Retry-After falls back to backoff", 3, []outcome{{status: statusBusy, retryAfter: "soon"}, ok}, statusOK, false, 2, 1},
		{"dial error is retried", 3, []outcome{{err: dial}, ok}, statusOK, false, 2, 1},
		{"read error after send is not retried", 3, []outcome{{err: reset}, ok}, 0, true, 1, 0},
		{"single attempt never retries", 1, []outcome{busy, ok}, statusBusy, false, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRetrier(t, tc.attempts, tc.outcomes...)
			if (got.err != nil) != tc.wantErr || got.status != tc.wantStatus {
				t.Fatalf("status = %d error = %v, want %d and error=%v", got.status, got.err, tc.wantStatus, tc.wantErr)
			}
			if len(got.bodies) != tc.wantCalls || got.retries != tc.wantRetries {
				t.Fatalf("calls = %d retries = %d, want %d and %d", len(got.bodies), got.retries, tc.wantCalls, tc.wantRetries)
			}
			for i, body := range got.bodies {
				if body != replayedPayload {
					t.Fatalf("attempt %d sent %q", i+1, body)
				}
			}
		})
	}
}

func TestRetrierWithoutReplayStatePassesThrough(t *testing.T) {
	upstream := &script{outcomes: []outcome{{status: statusBusy}, {status: statusOK}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://upstream.invalid/", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&retrier{base: upstream, attempts: 3, delay: time.Millisecond}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != statusBusy || len(upstream.bodies) != 1 {
		t.Fatalf("status = %d calls = %d", resp.StatusCode, len(upstream.bodies))
	}
}

func TestRetrierStopsWhenClientLeaves(t *testing.T) {
	upstream := &script{outcomes: []outcome{{status: statusBusy}}}
	state := &replay{body: []byte("x")}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), replayKey{}, state))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	started := time.Now()
	// A long delay proves the sleep is interrupted rather than waited out.
	resp, err := (&retrier{base: upstream, attempts: 5, delay: 4 * time.Second}).RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("error = %v after %s", err, time.Since(started))
	}
	if len(upstream.bodies) != 1 {
		t.Fatalf("calls = %d", len(upstream.bodies))
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		value  string
		want   time.Duration
		parsed bool
	}{
		{"", 0, false},
		{"soon", 0, false},
		{"-1", 0, false},
		{"0", 0, true},
		{"3", 3 * time.Second, true},
		{"9223372037", retryAfterCeiling, true},
		{"99999999999999999999", retryAfterCeiling, true},
		{"-99999999999999999999", 0, false},
		{now.Add(2 * time.Second).Format(http.TimeFormat), 2 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{now.Add(48 * time.Hour).Format(http.TimeFormat), retryAfterCeiling, true},
	}
	for _, tc := range cases {
		got, parsed := retryAfter(http.Header{"Retry-After": {tc.value}}, now)
		if got != tc.want || parsed != tc.parsed {
			t.Errorf("Retry-After %q = %s, %v; want %s, %v", tc.value, got, parsed, tc.want, tc.parsed)
		}
	}
}

func TestStalled503BodyDoesNotHoldTheRetry(t *testing.T) {
	release := make(chan struct{})
	var count atomicCounter
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if count.next() == 1 {
			w.WriteHeader(statusBusy)
			_, _ = w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			select { // headers sent, body never finishes
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer upstream.Close()
	defer close(release)
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authBearer, false)
	config.models[testModel].upstream.attempts = 2
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	started := time.Now()
	response, _ := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, `{"model":"`+testModel+`"}`))
	if response.StatusCode != statusOK || time.Since(started) > 3*time.Second {
		t.Fatalf("status = %d after %s", response.StatusCode, time.Since(started))
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) next() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

func TestBackoffStaysWithinBounds(t *testing.T) {
	r := &retrier{delay: retryBaseDelay}
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		ceiling := min(retryBaseDelay<<(attempt-1), retryMaxDelay)
		for range 200 {
			if got := r.backoff(attempt); got < ceiling/2 || got > ceiling {
				t.Fatalf("attempt %d: %s outside [%s, %s]", attempt, got, ceiling/2, ceiling)
			}
		}
	}
}

func TestRelayRetriesOverloadedUpstream(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		first := len(bodies) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(statusBusy)
			_, _ = w.Write([]byte(`{"error":{"message":"Service is too busy."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authBearer, false)
	config.models[testModel].upstream.attempts = 3
	var logs bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewTextHandler(&logs, nil)))
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	payload := `{"model":"` + testModel + `","messages":[]}`
	response, out := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, payload))
	if response.StatusCode != statusOK || string(out) != `{"type":"message"}` {
		t.Fatalf("status = %d body = %q", response.StatusCode, out)
	}
	if len(bodies) != 2 || bodies[0] != payload || bodies[1] != payload {
		t.Fatalf("upstream bodies = %q", bodies)
	}
	local.Close() // waits for the handler, so the request log line exists
	if !strings.Contains(logs.String(), "retries=1") {
		t.Fatalf("log lacks retry count: %s", logs.String())
	}
}

func TestRelayWithoutRetryReturnsFirst503(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(statusBusy)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authBearer, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	response, _ := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, `{"model":"`+testModel+`"}`))
	if response.StatusCode != statusBusy || calls != 1 {
		t.Fatalf("status = %d calls = %d", response.StatusCode, calls)
	}
}

func TestClientHangupIsNotAnUpstreamFailure(t *testing.T) {
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// net/http notices a closed connection only after the body is consumed.
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authBearer, false)
	var logs bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewTextHandler(&logs, nil)))
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	ctx, cancel := context.WithCancel(t.Context())
	req := networkRequest(t, local.URL, messagesPath, `{"model":"`+testModel+`"}`).WithContext(ctx)
	go func() { <-arrived; cancel() }()
	if response, err := local.Client().Do(req); err == nil {
		_ = response.Body.Close()
		t.Fatal("request should have been canceled")
	}
	local.Close() // waits for the handler, so the request log line exists
	if !strings.Contains(logs.String(), "status=499") || strings.Contains(logs.String(), "status=502") {
		t.Fatalf("hangup not logged as 499: %s", logs.String())
	}
}
