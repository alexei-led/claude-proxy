package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"testing"
)

func TestCredentialsInformationalResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Api-Key", "dummy-informational-key")
		w.Header().Set("Set-Cookie", "dummy-cookie")
		w.Header().Set("Link", "</safe>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("X-Api-Key")
		w.Header().Del("Set-Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	hints := make(chan http.Header, 1)
	trace := &httptrace.ClientTrace{Got1xxResponse: func(_ int, header textproto.MIMEHeader) error {
		hints <- http.Header(header).Clone()
		return nil
	}}
	req := networkRequest(t, local.URL, messagesPath, testBody)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	_, _ = roundTrip(t, local.Client(), req)
	select {
	case header := <-hints:
		if header.Get("X-Api-Key") != "" || header.Get("Set-Cookie") != "" {
			t.Fatal("informational credentials leaked")
		}
		if header.Get("Link") == "" {
			t.Fatal("safe informational header lost")
		}
	default:
		t.Fatal("informational response missing")
	}
}

func TestCredentialsUnannouncedTrailers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		connection, writer, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.WriteString(writer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\nX-Api-Key: dummy-unannounced-key\r\nX-Result: safe\r\n\r\n")
		_ = writer.Flush()
	}))
	defer upstream.Close()
	config := testConfig(t)
	configureUpstream(t, config, upstream.URL, authNone, false)
	relay := newTestRelay(t, config)
	defer relay.transport.CloseIdleConnections()
	local := httptest.NewServer(relay)
	defer local.Close()
	response, body := roundTrip(t, local.Client(), networkRequest(t, local.URL, messagesPath, testBody))
	if string(body) != "ok" || response.Trailer.Get("X-Api-Key") != "" {
		t.Fatal("unannounced trailer credential leaked")
	}
	if response.Trailer.Get("X-Result") != "safe" {
		t.Fatal("safe unannounced trailer lost")
	}
}
