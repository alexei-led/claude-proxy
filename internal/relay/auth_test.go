package relay

import (
	"net/http"
	"testing"
)

func TestLocalAccess(t *testing.T) {
	for _, values := range [][]string{nil, {""}, {"wrong"}, {"dummy-work-access"}, {testToken, testToken}, {testToken}} {
		header := make(http.Header)
		for _, value := range values {
			header.Add(localKeyHeader, value)
		}
		want := len(values) == 1 && values[0] == testToken
		if localAccess(header, testToken) != want {
			t.Fatalf("unexpected access decision for %d values", len(values))
		}
	}
}

func TestOutboundAuth(t *testing.T) {
	for _, mode := range []string{authOAuth, authBearer, authAPIKey, authNone} {
		t.Run(mode, func(t *testing.T) {
			in := make(http.Header)
			for _, key := range []string{localKeyHeader, "Cookie", "Proxy-Authorization", "X-Api-Key", "X-Workspace", "X-Organization", "X-Device-Id"} {
				in.Set(key, "dummy-client")
			}
			in.Set("Authorization", "Bearer dummy-oauth")
			in.Set("Anthropic-Beta", "oauth-beta")
			in.Set("Content-Type", "application/json")
			out := outboundHeaders(in, &upstream{mode: mode, key: testServerKey}, "Bearer dummy-oauth")
			if out.Get(localKeyHeader) != "" || out.Get("Cookie") != "" || out.Get("Proxy-Authorization") != "" {
				t.Fatal("local credentials leaked")
			}
			wantAuth, wantKey := "", ""
			switch mode {
			case authOAuth:
				wantAuth = "Bearer dummy-oauth"
			case authBearer:
				wantAuth = "Bearer dummy-server"
			case authAPIKey:
				wantKey = testServerKey
			}
			if out.Get("Authorization") != wantAuth || out.Get("X-Api-Key") != wantKey {
				t.Fatal("incorrect outbound credential")
			}
			if mode != authOAuth && (out.Get("X-Workspace") != "" || out.Get("X-Organization") != "" || out.Get("X-Device-Id") != "") {
				t.Fatal("identity leaked")
			}
			if out.Get("Anthropic-Beta") != "oauth-beta" {
				t.Fatal("beta header lost")
			}
			if in.Get(localKeyHeader) == "" {
				t.Fatal("inbound headers mutated")
			}
		})
	}
}
