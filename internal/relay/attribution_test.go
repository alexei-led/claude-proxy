package relay

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	servedModelHeader   = "X-Valar-Served-Model"
	attributionJSON     = "application/json"
	attributionSSE      = "text/event-stream"
	reportedHeaderModel = "served-model"
	reportedBodyModel   = "body-model"
)

func TestModelAttribution(t *testing.T) {
	cases := []struct {
		name, contentType, body, bodyModel string
		headers                            []string
		headerModel                        string
	}{
		{"json disagreement", attributionJSON, `{"model":"body-model","usage":{"input_tokens":5,"output_tokens":9}}`, reportedBodyModel, []string{"Org/served-Model:1[1m]"}, "Org/served-Model:1[1m]"},
		{"stream disagreement", attributionSSE, "event: message_start\ndata: {\"message\":{\"model\":\"body-model\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":9}}\n\n", reportedBodyModel, []string{reportedHeaderModel}, reportedHeaderModel},
		{"missing header", attributionJSON, `{"model":"body-model","usage":{"input_tokens":5,"output_tokens":9}}`, reportedBodyModel, nil, ""},
		{"header only", attributionJSON, `{"usage":{"input_tokens":5,"output_tokens":9}}`, "", []string{reportedHeaderModel}, reportedHeaderModel},
		{"empty header", attributionJSON, `{}`, "", []string{""}, ""},
		{"duplicate header", attributionJSON, `{}`, "", []string{"first", "second"}, ""},
		{"combined header", attributionJSON, `{}`, "", []string{"first,second"}, ""},
		{"malicious header", attributionJSON, `{}`, "", []string{"model?token=private-header"}, ""},
		{"spaces", attributionJSON, `{}`, "", []string{"Bearer private-key"}, ""},
		{"long header", attributionJSON, `{}`, "", []string{strings.Repeat("a", 129)}, ""},
		{"boundary body", attributionJSON, `{"model":"` + strings.Repeat("a", 128) + `","usage":{"input_tokens":5,"output_tokens":9}}`, strings.Repeat("a", 128), nil, ""},
		{"boundary header", attributionJSON, `{}`, "", []string{strings.Repeat("a", 128)}, strings.Repeat("a", 128)},
		{"malicious body", attributionJSON, `{"model":"bad\nspoof","usage":{"input_tokens":5,"output_tokens":9}}`, "", nil, ""},
		{"long body", attributionJSON, `{"model":"` + strings.Repeat("a", 129) + `","usage":{"input_tokens":5,"output_tokens":9}}`, "", nil, ""},
		{"invalid body model type", attributionJSON, `{"model":{"secret":"private-content"},"usage":{"input_tokens":5,"output_tokens":9}}`, "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			relay, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != testBody {
					t.Errorf("forwarded body changed: %q", body)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Unrelated-Private", "private-header")
				for _, value := range tc.headers {
					w.Header().Add(servedModelHeader, value)
				}
				_, _ = io.WriteString(w, tc.body)
			}, slog.LevelDebug)
			request := networkRequest(t, local.URL, messagesPath, testBody)
			request.Header.Set(servedModelHeader, "client-spoof")
			response, body := roundTrip(t, local.Client(), request)
			local.Close()
			if response.StatusCode != http.StatusOK || string(body) != tc.body || calls.Load() != 1 {
				t.Fatalf("response or call count changed: status=%d calls=%d body=%q", response.StatusCode, calls.Load(), body)
			}
			if got := response.Header.Values(servedModelHeader); strings.Join(got, ",") != strings.Join(tc.headers, ",") {
				t.Fatalf("response headers changed: %q", got)
			}
			records := jsonRecords(t, logs, "request")
			if len(records) != 1 {
				t.Fatalf("records = %v", records)
			}
			record := records[0]
			for key, want := range map[string]string{
				"model": testModel, "forwarded_model": testModel,
				"body_reported_model": tc.bodyModel, "header_reported_model": tc.headerModel,
			} {
				if want == "" {
					if _, ok := record[key]; ok {
						t.Errorf("unexpected %s = %v", key, record[key])
					}
				} else if record[key] != want {
					t.Errorf("%s = %v, want %q", key, record[key], want)
				}
			}
			stats := relay.Stats()
			if len(stats.Routes) != 1 || stats.Routes[0].Model != testModel || stats.Routes[0].Requests != 1 {
				t.Fatalf("stats = %+v", stats)
			}
			if strings.Contains(tc.body, "input_tokens") && (stats.Routes[0].InputTokens != 5 || stats.Routes[0].OutputTokens != 9) {
				t.Fatalf("attribution changed token accounting: %+v", stats)
			}
			if strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "client-spoof") {
				t.Fatal("unrelated header, content or client attribution leaked")
			}
		})
	}
}

func TestAttributionDoesNotChangeStatsCardinality(t *testing.T) {
	var calls atomic.Int32
	relay, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", attributionJSON)
		model := strings.Repeat("m", int(calls.Add(1)))
		w.Header().Set(servedModelHeader, model)
		var body struct {
			Model string `json:"model"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		body.Model = model
		body.Usage.Input, body.Usage.Output = 5, 9
		_ = json.NewEncoder(w).Encode(body)
	}, slog.LevelDebug)
	for range 3 {
		roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	}
	local.Close()
	stats := relay.Stats()
	if len(stats.Routes) != 1 || stats.Routes[0].Requests != 3 || stats.Routes[0].InputTokens != 15 || stats.Routes[0].OutputTokens != 27 {
		t.Fatalf("stats = %+v", stats)
	}
	for i, record := range jsonRecords(t, logs, "request") {
		want := strings.Repeat("m", i+1)
		if record["header_reported_model"] != want || record["body_reported_model"] != want {
			t.Errorf("request %d attribution = %v", i, record)
		}
	}
}

func TestLocalRejectionHasNoForwardedAttribution(t *testing.T) {
	_, local, logs := observedRelay(t, func(http.ResponseWriter, *http.Request) {
		t.Error("rejected request reached upstream")
	}, slog.LevelDebug)
	request := networkRequest(t, local.URL, messagesPath, testBody)
	request.Header.Set(localKeyHeader, "wrong-key")
	roundTrip(t, local.Client(), request)
	local.Close()
	records := jsonRecords(t, logs, "request")
	if len(records) != 1 {
		t.Fatalf("records = %v", records)
	}
	for _, key := range []string{"forwarded_model", "body_reported_model", "header_reported_model"} {
		if _, ok := records[0][key]; ok {
			t.Errorf("local rejection has %s", key)
		}
	}
}

func TestReportedModelControlsAreOmitted(t *testing.T) {
	for _, value := range []string{"bad\r\nspoof", "bad\tvalue", "bad\x00value", "bad\x1b[2J", "модель", "model?token=private"} {
		t.Run(value, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			summary := &responseSummary{ResponseWriter: recorder, forwarded: true}
			summary.Header().Set(servedModelHeader, value)
			summary.WriteHeader(http.StatusOK)
			config := testConfig(t)
			var logs strings.Builder
			relay := NewServer(config, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer relay.transport.CloseIdleConnections()
			relay.logRequest(t.Context(), summary, http.MethodPost, kindMessages, outcomeOK, 0)
			if strings.Contains(logs.String(), "header_reported_model") {
				t.Fatalf("unsafe value logged: %s", logs.String())
			}
		})
	}
}
