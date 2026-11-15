package relay

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestBodyAttributionUsesOnlyMessageModel(t *testing.T) {
	cases := []struct {
		name, media, body, want string
	}{
		{"stream", attributionSSE, "event: message_start\ndata: {\"message\":{\"model\":\"reported-model\"}}\n\nevent: content_block_delta\ndata: {\"message\":{\"model\":\"content-spoof\"}}\n\nevent: message_delta\ndata: {\"message\":{\"model\":\"delta-spoof\"}}\n\n", "reported-model"},
		{"unsafe stream", attributionSSE, "event: message_start\ndata: {\"message\":{\"model\":\"bad\\nspoof\"}}\n\n", ""},
		{"invalid stream type", attributionSSE, "event: message_start\ndata: {\"message\":{\"model\":123}}\n\n", ""},
		{"oversized stream line", attributionSSE, "event: message_start\ndata: {\"message\":{\"model\":\"reported-model\"},\"pad\":\"" + strings.Repeat("a", maxTapLineBytes) + "\"}\n\n", ""},
		{"oversized json", attributionJSON, `{"model":"reported-model","pad":"` + strings.Repeat("a", maxTapJSONBytes) + `"}`, ""},
		{"json content only", attributionJSON, `{"content":[{"model":"content-spoof"}]}`, ""},
		{"opaque body", "text/plain", `{"model":"content-spoof"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunk := range []int{1, 7, len(tc.body)} {
				tap := &usageTap{}
				tap.begin(contentType(tc.media))
				for start := 0; start < len(tc.body); start += chunk {
					tap.write([]byte(tc.body[start:min(start+chunk, len(tc.body))]))
				}
				tap.finish()
				if tap.model != tc.want {
					t.Errorf("chunk %d: model = %q, want %q", chunk, tap.model, tc.want)
				}
			}
		})
	}
}

func TestHeaderAttributionUsesFinalHeadersOnly(t *testing.T) {
	for _, final := range []string{"final-model", ""} {
		t.Run(final, func(t *testing.T) {
			relay, local, logs := observedRelay(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set(servedModelHeader, "early-spoof")
				w.WriteHeader(http.StatusEarlyHints)
				w.Header().Del(servedModelHeader)
				if final != "" {
					w.Header().Set(servedModelHeader, final)
				}
				w.Header().Set("Trailer", servedModelHeader)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, "provider-error")
				w.Header().Set(servedModelHeader, "trailer-spoof")
			}, slog.LevelDebug)
			response, body := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
			local.Close()
			if response.StatusCode != http.StatusTooManyRequests || string(body) != "provider-error" || !strings.Contains(strings.Join(response.Trailer.Values(servedModelHeader), ","), "trailer-spoof") {
				t.Fatalf("response changed: status=%d body=%q trailers=%v", response.StatusCode, body, response.Trailer)
			}
			records := jsonRecords(t, logs, "request")
			if len(records) != 1 || records[0]["outcome"] != outcomeUpstreamErr {
				t.Fatalf("records = %v", records)
			}
			got, exists := records[0]["header_reported_model"]
			if final == "" && exists || final != "" && got != final {
				t.Errorf("final attribution = %v, want %q", got, final)
			}
			if strings.Contains(logs.String(), "spoof") || relay.Stats().Routes[0].Requests != 1 {
				t.Fatal("informational/trailer claim leaked or request counted more than once")
			}
		})
	}
}
