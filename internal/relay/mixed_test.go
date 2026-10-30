package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCrossProviderTurns(t *testing.T) {
	profile := mixedProfile(t)
	fixture, err := os.ReadFile("testdata/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{personalExample, workExample} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			captures := make(chan capture, 5)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Error(readErr)
					return
				}
				captures <- capture{body: string(body), query: r.URL.RawQuery, path: r.URL.Path, header: r.Header.Clone()}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": ping\n\nevent: unfamiliar\ndata: opaque\n\nevent: message_stop\ndata: {}\n\n")
			}))
			defer upstream.Close()
			raw := exampleConfig(t, name)
			for label, entry := range raw.Upstreams {
				entry.MessagesURL = upstream.URL + "/" + label + messagesPath
				entry.CountTokensURL = ""
				raw.Upstreams[label] = entry
			}
			config := materializeConfig(t, raw)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			for _, tier := range []string{"high", "medium", "low", "micro", "high"} {
				id := routerAPIModelID(profile.Models[profile.Routes[tier].Model].ID)
				encoded, marshalErr := json.Marshal(id)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				body := strings.Replace(string(fixture), `"model": "exact-model"`, `"model": `+string(encoded), 1)
				req := networkRequest(t, local.URL, messagesPath+"?beta=true", body)
				req.Host = config.listen
				req.Header.Set(localKeyHeader, config.token)
				req.Header.Set("Authorization", "Bearer dummy-"+raw.Profile+"-oauth")
				req.Header.Set("X-Api-Key", "dummy-client-key")
				req.Header.Set("X-Organization", "dummy-"+raw.Profile+"-organization")
				req.Header.Set("X-Device-Id", "dummy-device")
				req.Header.Set("Connection", "Authorization, X-Api-Key, X-Claude-Proxy-Key")
				response, stream := roundTrip(t, local.Client(), req)
				if response.StatusCode != http.StatusOK || string(stream) != ": ping\n\nevent: unfamiliar\ndata: opaque\n\nevent: message_stop\ndata: {}\n\n" {
					t.Fatal("stream changed")
				}
				captured := <-captures
				entry := config.models[id]
				if captured.path != "/"+entry.label+messagesPath || captured.body != body || captured.query != "beta=true" {
					t.Fatal("wrong provider or mutated native request")
				}
				wantAuth, wantKey := "", ""
				switch entry.upstream.mode {
				case authOAuth:
					wantAuth = "Bearer dummy-" + raw.Profile + "-oauth"
				case authBearer:
					wantAuth = "Bearer " + entry.upstream.key
				case authAPIKey:
					wantKey = entry.upstream.key
				}
				if captured.header.Get("Authorization") != wantAuth || captured.header.Get("X-Api-Key") != wantKey {
					t.Fatal("wrong profile/provider credential")
				}
				if captured.header.Get(localKeyHeader) != "" {
					t.Fatal("local token leaked")
				}
				if entry.upstream.mode != authOAuth && (captured.header.Get("X-Organization") != "" || captured.header.Get("X-Device-Id") != "") {
					t.Fatal("session identity leaked to foreign provider")
				}
			}
			wrong := networkRequest(t, local.URL, messagesPath, string(fixture))
			wrong.Host = config.listen
			wrong.Header.Set(localKeyHeader, "dummy-other-profile")
			response, _ := roundTrip(t, local.Client(), wrong)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatal("cross-profile key accepted")
			}
			select {
			case <-captures:
				t.Fatal("rejected profile reached upstream")
			default:
			}
		})
	}
}
