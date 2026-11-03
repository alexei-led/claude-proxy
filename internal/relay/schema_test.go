package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStripToolPatterns(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "nested schema patterns",
			in:   `{"model":"m","tools":[{"name":"Artifact","input_schema":{"type":"object","properties":{"file_paths":{"type":"array","items":{"type":"string","pattern":"^[^\\0]*$"}},"after":{"type":"string","pattern":"^a$"},"contract":{"anyOf":[{"type":"null"},{"type":"string","pattern":"^\\d$"}]}}}}],"stream":true}`,
			want: `{"model":"m","tools":[{"name":"Artifact","input_schema":{"type":"object","properties":{"file_paths":{"type":"array","items":{"type":"string"}},"after":{"type":"string"},"contract":{"anyOf":[{"type":"null"},{"type":"string"}]}}}}],"stream":true}`,
		},
		{
			name: "property named pattern is kept",
			in:   `{"model":"m","tools":[{"name":"Grep","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}}]}`,
			want: `{"model":"m","tools":[{"name":"Grep","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}}]}`,
		},
		{
			name: "data keywords are kept",
			in:   `{"model":"m","tools":[{"name":"x","input_schema":{"type":"object","default":{"pattern":"a"},"$defs":{"id":{"type":"string","pattern":"b"}}}}]}`,
			want: `{"model":"m","tools":[{"name":"x","input_schema":{"type":"object","default":{"pattern":"a"},"$defs":{"id":{"type":"string"}}}}]}`,
		},
		{
			name: "no tools keeps bytes",
			in:   " {\"model\" : \"m\", \"messages\":[{\"pattern\":\"x\"}]} \n",
			want: " {\"model\" : \"m\", \"messages\":[{\"pattern\":\"x\"}]} \n",
		},
		{
			name: "server tool without schema keeps bytes",
			in:   `{"model":"m","tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			want: `{"model":"m","tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(stripToolPatterns([]byte(tc.in))); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestToolPatternsByAuthMode(t *testing.T) {
	const in = `{"model":"exact-model","tools":[{"name":"t","input_schema":{"type":"string","pattern":"^x$"}}]}`
	const stripped = `{"model":"exact-model","tools":[{"name":"t","input_schema":{"type":"string"}}]}`
	for mode, want := range map[string]string{authOAuth: in, authBearer: stripped, authAPIKey: stripped, authNone: stripped} {
		t.Run(mode, func(t *testing.T) {
			captured := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				captured <- string(body)
			}))
			defer upstream.Close()
			config := testConfig(t)
			configureUpstream(t, config, upstream.URL, mode, true)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			local := httptest.NewServer(relay)
			defer local.Close()
			for _, path := range []string{messagesPath, countTokensPath} {
				_, _ = roundTrip(t, local.Client(), networkRequest(t, local.URL, path, in))
				if got := <-captured; got != want {
					t.Fatalf("%s forwarded %s", path, got)
				}
			}
		})
	}
}
