package relay

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardUnsolicitedUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "upgrade")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	out := httptest.NewRecorder()
	relay.ServeHTTP(out, request(http.MethodPost, messagesPath, testBody))
	if out.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", out.Code)
	}
}

func TestCredentialsProfileIsolation(t *testing.T) {
	for _, profile := range []string{personalProfile, workProfile} {
		t.Run(profile, func(t *testing.T) {
			captured := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured <- r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			config := testConfig(t)
			config.profile = profile
			config.token = "dummy-" + profile + "-access"
			configureUpstream(t, config, upstream.URL, authOAuth, false)
			relay := newTestRelay(t, config)
			defer relay.transport.CloseIdleConnections()
			req := request(http.MethodPost, messagesPath, testBody)
			req.Header.Set(localKeyHeader, config.token)
			req.Header.Set("Authorization", "Bearer dummy-"+profile+"-oauth")
			out := httptest.NewRecorder()
			relay.ServeHTTP(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("status=%d", out.Code)
			}
			got := <-captured
			if got.Get("Authorization") != "Bearer dummy-"+profile+"-oauth" {
				t.Fatal("wrong profile OAuth")
			}
			other := personalProfile
			if profile == other {
				other = workProfile
			}
			req = request(http.MethodPost, messagesPath, testBody)
			req.Header.Set(localKeyHeader, "dummy-"+other+"-access")
			out = httptest.NewRecorder()
			relay.ServeHTTP(out, req)
			if out.Code != http.StatusUnauthorized {
				t.Fatal("cross-profile key accepted")
			}
			select {
			case <-captured:
				t.Fatal("cross-profile request forwarded")
			default:
			}
		})
	}
}

func TestCredentialsLogs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "dummy-provider-body")
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authBearer, false)
	var output bytes.Buffer
	relay := NewServer(config, slog.New(slog.NewJSONHandler(&output, nil)))
	defer relay.transport.CloseIdleConnections()
	out := httptest.NewRecorder()
	req := request(http.MethodPost, messagesPath, `{"model":"exact-model","metadata":"dummy-request-body"}`)
	req.Header.Set("X-Api-Key", "dummy-client-key")
	relay.ServeHTTP(out, req)
	unknown := request(http.MethodGet, "/dummy-private-path?payload=dummy-query", "")
	relay.ServeHTTP(httptest.NewRecorder(), unknown)
	for _, forbidden := range []string{upstream.URL, testToken, testServerKey, "dummy-oauth", "dummy-client-key", "dummy-request-body", "dummy-provider-body", "dummy-private-path", "dummy-query"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("sensitive value logged: %s", forbidden)
		}
	}
	if !strings.Contains(output.String(), testModel) || !strings.Contains(output.String(), `"status":401`) {
		t.Fatal("missing request summary")
	}
}
