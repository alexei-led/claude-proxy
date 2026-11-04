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
	"time"
)

func TestRequestOutcome(t *testing.T) {
	cases := []struct {
		name                string
		summary             responseSummary
		aborted, clientGone bool
		want                string
	}{
		{"success", responseSummary{status: 200, forwarded: true}, false, false, outcomeOK},
		{"catalog", responseSummary{status: 200}, false, false, outcomeOK},
		{"local auth", responseSummary{status: 401, category: "authentication_error"}, false, false, outcomeRejected},
		{"local capacity", responseSummary{status: 429, category: "rate_limit_error"}, false, false, outcomeBusy},
		{"draining", responseSummary{status: 503, category: apiError}, false, false, outcomeBusy},
		{"provider 429", responseSummary{status: 429, forwarded: true}, false, false, outcomeUpstreamErr},
		{"provider 529", responseSummary{status: 529, forwarded: true}, false, false, outcomeUpstreamErr},
		{"stream error event", responseSummary{status: 200, forwarded: true, usage: &usageTap{errorType: "overloaded_error"}}, false, false, outcomeUpstreamErr},
		{"unreachable", responseSummary{status: 502, forwarded: true, category: apiError}, false, false, outcomeUnreachable},
		{"timeout", responseSummary{status: 504, forwarded: true, category: "timeout_error"}, false, false, outcomeTimeout},
		{"hangup before headers", responseSummary{status: 499, forwarded: true, category: apiError}, false, true, outcomeClientClosed},
		{"stream cut by upstream", responseSummary{status: 200, forwarded: true}, true, false, outcomeBroken},
		{"stream cut by client", responseSummary{status: 200, forwarded: true}, true, true, outcomeClientClosed},
		{"client write failed", responseSummary{status: 200, forwarded: true, writeFailed: true}, true, false, outcomeClientClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestOutcome(&tc.summary, tc.aborted, tc.clientGone); got != tc.want {
				t.Fatalf("outcome = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRequestLevel(t *testing.T) {
	cases := []struct {
		kind, result string
		retries      int
		want         slog.Level
	}{
		{kindMessages, outcomeOK, 0, slog.LevelDebug},
		{kindCountTokens, outcomeOK, 0, LevelTrace},
		{kindModels, outcomeOK, 0, LevelTrace},
		{kindMessages, outcomeOK, 1, slog.LevelInfo},
		{kindMessages, outcomeClientClosed, 0, slog.LevelInfo},
		{kindMessages, outcomeBroken, 0, slog.LevelWarn},
		{kindMessages, outcomeUpstreamErr, 0, slog.LevelWarn},
		{kindModels, outcomeRejected, 0, slog.LevelWarn},
		{kindMessages, outcomeBusy, 0, slog.LevelWarn},
	}
	for _, tc := range cases {
		if got := requestLevel(tc.kind, tc.result, tc.retries); got != tc.want {
			t.Errorf("%s/%s/%d level = %v, want %v", tc.kind, tc.result, tc.retries, got, tc.want)
		}
	}
}

const usageStream = "event: message_start\r\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"output_tokens":1}}}` + "\r\n\r\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"text":"\"usage\":{\"output_tokens\":999} message_delta"}}` + "\n\n" +
	": ping\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":56}}` + "\n\n" +
	"event: message_stop\ndata: {}\n\n"

func contentType(value string) http.Header { return http.Header{"Content-Type": {value}} }

func TestUsageTapReadsStreamInAnyChunking(t *testing.T) {
	for _, size := range []int{1, 7, 64, len(usageStream)} {
		tap := &usageTap{}
		tap.begin(contentType("text/event-stream; charset=utf-8"))
		for start := 0; start < len(usageStream); start += size {
			tap.write([]byte(usageStream[start:min(start+size, len(usageStream))]))
		}
		tap.finish()
		want := tokenCounts{input: 12, output: 56, cacheRead: 300, cacheWrite: 40}
		if !tap.seen || tap.tokens != want || tap.stopReason != "max_tokens" || tap.errorType != "" {
			t.Fatalf("chunk %d: tokens=%+v stop=%q error=%q", size, tap.tokens, tap.stopReason, tap.errorType)
		}
	}
}

func TestUsageTapStreamErrorAndLimits(t *testing.T) {
	tap := &usageTap{}
	tap.begin(contentType("text/event-stream"))
	huge := "event: message_delta\ndata: {\"usage\":{\"output_tokens\":7},\"pad\":\"" + strings.Repeat("x", maxTapLineBytes) + "\"}\n\n"
	tap.write([]byte(huge))
	tap.write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"secret detail\"}}\n\n"))
	if tap.seen || tap.errorType != "overloaded_error" {
		t.Fatalf("oversized line parsed or error missed: seen=%v error=%q", tap.seen, tap.errorType)
	}
	tap = &usageTap{}
	tap.begin(contentType("text/event-stream"))
	tap.write([]byte("event: error\ndata: {\"error\":{\"type\":\"Bad Type\\u001b[2J\"}}\n\n"))
	if tap.errorType != "" {
		t.Fatalf("unsafe provider label kept: %q", tap.errorType)
	}
}

func TestUsageTapJSONResponses(t *testing.T) {
	cases := []struct {
		name, body          string
		wantTokens          tokenCounts
		wantStop, wantError string
		wantSeen            bool
	}{
		{"message", `{"type":"message","content":[],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":9}}`, tokenCounts{input: 5, output: 9}, "end_turn", "", true},
		{"error", `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`, tokenCounts{}, "", "invalid_request_error", false},
		{"not json", `<html>bad gateway</html>`, tokenCounts{}, "", "", false},
		{"too large", `{"usage":{"input_tokens":1},"pad":"` + strings.Repeat("x", maxTapJSONBytes) + `"}`, tokenCounts{}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tap := &usageTap{}
			tap.begin(contentType("application/json"))
			half := len(tc.body) / 2
			tap.write([]byte(tc.body[:half]))
			tap.write([]byte(tc.body[half:]))
			tap.finish()
			if tap.tokens != tc.wantTokens || tap.stopReason != tc.wantStop || tap.errorType != tc.wantError || tap.seen != tc.wantSeen {
				t.Fatalf("tokens=%+v stop=%q error=%q seen=%v", tap.tokens, tap.stopReason, tap.errorType, tap.seen)
			}
		})
	}
}

func TestUpstreamRequestID(t *testing.T) {
	const anthropicID, genericID = "Request-Id", "X-Request-Id"
	cases := []struct {
		header http.Header
		want   string
	}{
		{http.Header{anthropicID: {"req_011CZ-abc.1:2"}}, "req_011CZ-abc.1:2"},
		{http.Header{genericID: {"gen-123"}}, "gen-123"},
		{http.Header{anthropicID: {"bad id\n"}, genericID: {"fallback"}}, "fallback"},
		{http.Header{anthropicID: {strings.Repeat("a", 129)}}, ""},
		{http.Header{}, ""},
	}
	for _, tc := range cases {
		if got := upstreamRequestID(tc.header); got != tc.want {
			t.Errorf("request ID = %q, want %q", got, tc.want)
		}
	}
}

func TestHistogramQuantile(t *testing.T) {
	var h histogram
	if h.quantile(0.5) != 0 {
		t.Fatal("empty histogram reported latency")
	}
	for _, d := range []time.Duration{50 * time.Millisecond, 80 * time.Millisecond, 400 * time.Millisecond, 3 * time.Second} {
		h.add(d)
	}
	if got := h.quantile(0.5); got != 100*time.Millisecond {
		t.Fatalf("p50 = %v", got)
	}
	if got := h.quantile(0.95); got != 5*time.Second {
		t.Fatalf("p95 = %v", got)
	}
	h.add(time.Hour)
	if got := h.quantile(1); got != 5*time.Minute {
		t.Fatalf("open bucket = %v", got)
	}
}

// jsonRecords decodes JSON log lines with a given message.
func jsonRecords(t *testing.T, logs *bytes.Buffer, message string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid log line %q: %v", line, err)
		}
		if record["msg"] == message {
			records = append(records, record)
		}
	}
	return records
}

func observedRelay(t *testing.T, handler http.HandlerFunc, level slog.Level) (*Server, *httptest.Server, *bytes.Buffer) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, true)
	logs := &bytes.Buffer{}
	relay := NewServer(config, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level, ReplaceAttr: NameLevel})))
	t.Cleanup(relay.transport.CloseIdleConnections)
	local := httptest.NewServer(relay)
	t.Cleanup(local.Close)
	return relay, local, logs
}

func TestStreamAccessRecordCarriesUsageAndTiming(t *testing.T) {
	relay, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_dummy_1")
		_, _ = io.WriteString(w, usageStream)
	}, slog.LevelDebug)
	response, body := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	if response.StatusCode != http.StatusOK || string(body) != usageStream {
		t.Fatalf("stream changed: %d", response.StatusCode)
	}
	local.Close() // waits for the handler, so the record exists
	records := jsonRecords(t, logs, "request")
	if len(records) != 1 {
		t.Fatalf("records = %v", records)
	}
	record := records[0]
	for key, want := range map[string]any{
		"level": "DEBUG", "outcome": outcomeOK, "upstream_request_id": "req_dummy_1", "stop_reason": "max_tokens",
		"input_tokens": 12.0, "output_tokens": 56.0, "cache_read_tokens": 300.0, "cache_write_tokens": 40.0,
	} {
		if record[key] != want {
			t.Errorf("%s = %v, want %v", key, record[key], want)
		}
	}
	if _, ok := record["ttfb"]; !ok {
		t.Error("ttfb missing")
	}
	stats := relay.Stats()
	if len(stats.Routes) != 1 || stats.Routes[0].OutputTokens != 56 || stats.Routes[0].Outcomes[outcomeOK] != 1 || stats.Routes[0].TTFBP50 == "" {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestDefaultLevelOmitsRoutineSuccess(t *testing.T) {
	_, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "/count") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":3}`)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}, slog.LevelInfo)
	catalog, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+modelsPath, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Host = testHost
	catalog.Header.Set(localKeyHeader, testToken)
	roundTrip(t, local.Client(), catalog)
	roundTrip(t, local.Client(), networkRequest(t, local.URL, countTokensPath, testBody))
	roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	local.Close()
	records := jsonRecords(t, logs, "request")
	if len(records) != 1 || records[0]["outcome"] != outcomeUpstreamErr || records[0]["level"] != "WARN" || records[0]["status"] != 429.0 {
		t.Fatalf("records = %v", records)
	}
}

func TestBrokenStreamIsNotLoggedAsSuccess(t *testing.T) {
	relay, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {}\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		connection, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}, slog.LevelInfo)
	response, err := local.Client().Do(networkRequest(t, local.URL, messagesPath, testBody))
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	local.Close()
	records := jsonRecords(t, logs, "request")
	if len(records) != 1 || records[0]["outcome"] != outcomeBroken || records[0]["status"] != 200.0 || records[0]["level"] != "WARN" {
		t.Fatalf("records = %v", records)
	}
	if relay.Stats().Routes[0].Outcomes[outcomeBroken] != 1 {
		t.Fatal("broken stream not counted")
	}
}

func TestRetryReasonsAreLogged(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) < 3 {
			w.WriteHeader(statusOverloaded)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	config.models[testModel].upstream.attempts = 3
	var logs bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewJSONHandler(&logs, nil)))
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	local.Close()
	records := jsonRecords(t, &logs, "request")
	if len(records) != 1 || records[0]["retry_reasons"] != "529,529" || records[0]["retries"] != 2.0 || records[0]["level"] != "INFO" {
		t.Fatalf("records = %v", records)
	}
}

func TestStatsSummaryLogsOnlyNewTraffic(t *testing.T) {
	config := testConfig(t)
	var logs bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewJSONHandler(&logs, nil)))
	record := func(kind, result string, ttfb time.Duration, tokens tokenCounts) {
		summary := &responseSummary{model: testModel, upstream: "u", forwarded: kind != kindModels, ttfb: ttfb,
			usage: &usageTap{seen: tokens != tokenCounts{}, tokens: tokens}}
		relay.stats.record(kind, summary, result, 2*ttfb)
	}
	record(kindMessages, outcomeOK, 300*time.Millisecond, tokenCounts{input: 10, output: 20})
	record(kindMessages, outcomeUpstreamErr, 80*time.Millisecond, tokenCounts{})
	record(kindMessages, outcomeClientClosed, 0, tokenCounts{})
	record(kindModels, outcomeOK, 0, tokenCounts{})
	relay.SetClients(2, true)
	first := relay.stats.copyRoutes()
	relay.logStats(t.Context(), map[routeKey]routeCounts{}, first, time.Minute)
	relay.logStats(t.Context(), first, relay.stats.copyRoutes(), time.Minute)
	records := jsonRecords(t, &logs, "stats")
	if len(records) != 1 {
		t.Fatalf("want one summary for the messages route only, got %v", records)
	}
	for key, want := range map[string]any{
		"registered_clients": 2.0, "tracking_uncertain": true, "active_requests": 0.0,
		"requests": 3.0, "failed": 1.0, "client_closed": 1.0, "input_tokens": 10.0, "output_tokens": 20.0,
		"ttfb_p50": float64(100 * time.Millisecond), "ttfb_p95": float64(500 * time.Millisecond),
	} {
		if records[0][key] != want {
			t.Errorf("%s = %v, want %v", key, records[0][key], want)
		}
	}
}

func TestStatsReporterLogsRemainderOnStop(t *testing.T) {
	config := testConfig(t)
	var logs bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewJSONHandler(&logs, nil)))
	relay.statsEvery = time.Hour
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); relay.reportStats(t.Context(), stop) }()
	relay.stats.record(kindMessages, &responseSummary{model: testModel, upstream: "u", forwarded: true}, outcomeOK, time.Second)
	close(stop)
	<-done
	if records := jsonRecords(t, &logs, "stats"); len(records) != 1 || records[0]["requests"] != 1.0 {
		t.Fatalf("final summary missing: %s", logs.String())
	}
}

func TestLogLevelConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
		valid bool
	}{
		{"", slog.LevelError, true}, // the fallback passed below
		{"trace", LevelTrace, true},
		{"debug", slog.LevelDebug, true},
		{"warn", slog.LevelWarn, true},
		{"verbose", 0, false},
		{"INFO", 0, false},
	} {
		path := configFixture(t, func(body string) string {
			if tc.value == "" {
				return body
			}
			return strings.Replace(body, versionFragment, `"runtime":{"log_level":"`+tc.value+`"},`+versionFragment, 1)
		})
		config, err := LoadConfig(path)
		if (err == nil) != tc.valid {
			t.Fatalf("%q: err = %v", tc.value, err)
		}
		if err == nil && config.LogLevel(slog.LevelError) != tc.want {
			t.Fatalf("%q: level = %v", tc.value, config.LogLevel(slog.LevelError))
		}
	}
}

func TestTraceLevelIsNamed(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: LevelTrace, ReplaceAttr: NameLevel}))
	logger.Log(t.Context(), LevelTrace, "request")
	if !strings.Contains(logs.String(), `"level":"TRACE"`) {
		t.Fatalf("trace level unnamed: %s", logs.String())
	}
}
