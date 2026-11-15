package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardedModelOmitsUnrepresentableIdentity(t *testing.T) {
	boundary := strings.Repeat("m", 128)
	for _, tc := range []struct {
		name, id, display, forwarded string
	}{
		{"boundary", boundary, boundary, boundary},
		{"long", boundary + "z", boundary + "...", ""},
		{"literal ellipsis", strings.Repeat("m", 125) + "...", strings.Repeat("m", 125) + "...", strings.Repeat("m", 125) + "..."},
		{"unsafe", "unsafe\nmodel", "unsafe\nmodel", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encodedID, err := json.Marshal(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			requestBody := `{"model":` + string(encodedID) + `,"messages":[]}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if string(body) != requestBody {
					t.Errorf("forwarded request changed: %q", body)
				}
				w.Header().Set("Content-Type", attributionJSON)
				_, _ = io.WriteString(w, `{"usage":{"input_tokens":5,"output_tokens":9}}`)
			}))
			defer upstream.Close()
			config, err := LoadConfig(configFixture(t, func(body string) string {
				body = strings.Replace(body, `"exact-model"`, string(encodedID), 1)
				body = strings.Replace(body, "http://127.0.0.1:12345/native/messages", upstream.URL+"/native/messages", 1)
				return strings.Replace(body, oauthFragment, `"mode":"none"`, 1)
			}))
			if err != nil {
				t.Fatalf("recognized model rejected by configuration: %v", err)
			}
			var logs bytes.Buffer
			relay := NewServer(config, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			response, body := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, requestBody))
			local.Close()
			if response.StatusCode != http.StatusOK || string(body) != `{"usage":{"input_tokens":5,"output_tokens":9}}` {
				t.Fatalf("response changed: %d %q", response.StatusCode, body)
			}
			records := jsonRecords(t, &logs, "request")
			if len(records) != 1 || records[0]["model"] != tc.display {
				t.Fatalf("display label changed: %v", records)
			}
			forwarded, exists := records[0]["forwarded_model"]
			if tc.forwarded == "" && exists || tc.forwarded != "" && forwarded != tc.forwarded {
				t.Errorf("forwarded_model = %v (present=%v), want %q", forwarded, exists, tc.forwarded)
			}
			stats := relay.Stats()
			if len(stats.Routes) != 1 || stats.Routes[0].Model != tc.display || stats.Routes[0].Requests != 1 ||
				stats.Routes[0].InputTokens != 5 || stats.Routes[0].OutputTokens != 9 {
				t.Fatalf("stats changed: %+v", stats)
			}
		})
	}
}
