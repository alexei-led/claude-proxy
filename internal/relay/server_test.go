package relay

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testHost        = "127.0.0.1:43171"
	testToken       = "dummy-personal-access"
	testModel       = "exact-model"
	testServerKey   = "dummy-server"
	testSecret      = "dummy-key"
	versionFragment = `"version":1`
	oauthFragment   = `"mode":"oauth_passthrough"`
)

func testConfig(t *testing.T) *Config {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected upstream call")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(fake.Close)
	config, err := LoadConfig(configFixture(t, func(body string) string {
		return strings.Replace(body, "http://127.0.0.1:12345/native/messages", fake.URL+"/native/messages", 1)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func newTestRelay(t *testing.T, config *Config) *Server {
	t.Helper()
	return NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(method, path, body string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, "http://"+testHost+path, strings.NewReader(body))
	req.Header.Set(localKeyHeader, testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dummy-oauth")
	return req
}

func TestHTTPBoundary(t *testing.T) {
	server := newTestRelay(t, testConfig(t))
	cases := []struct {
		name, method, path, body string
		edit                     func(*http.Request)
		status                   int
	}{
		{"missing key", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Del(localKeyHeader) }, 401},
		{"wrong key", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Set(localKeyHeader, "dummy-work-access") }, 401},
		{"duplicate key", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Add(localKeyHeader, testToken) }, 401},
		{"origin", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"fetch", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, 403},
		{"host", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Host = "evil.invalid" }, 403},
		{"cookie", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Set("Cookie", "key=value") }, 400},
		{"semicolon query key", http.MethodGet, "/v1/models?trace=a;token=bad", "", nil, 400},
		{"encoded query key", http.MethodGet, "/v1/models?%58-Claude-Proxy-Key=bad", "", nil, 400},
		{"invalid UTF8", http.MethodPost, messagesPath, "{\"model\":\"exact-model\",\"unknown\":\"\xff\"}", nil, 400},
		{"query key", http.MethodGet, "/v1/models?X-Claude-Proxy-Key=bad", "", nil, 400},
		{"camel-case API query key", http.MethodGet, "/v1/models?apiKey=bad", "", nil, 400},
		{"compact API query key", http.MethodGet, "/v1/models?apikey=bad", "", nil, 400},
		{"encoded API query key", http.MethodGet, "/v1/models?api%4Bey=bad", "", nil, 400},
		{"semicolon API query key", http.MethodGet, "/v1/models?trace=a;apiKey=bad", "", nil, 400},
		{"upgrade", http.MethodPost, messagesPath, testBody, func(r *http.Request) { r.Header.Set("Upgrade", "websocket"); r.Header.Set("Connection", "upgrade") }, 400},
		{"probe", http.MethodHead, "/api/hello", "", nil, 404},
		{"method", http.MethodGet, messagesPath, "", nil, 405},
		{"encoded path", http.MethodGet, "/v1/%6dodels", "", nil, 404},
		{"media", http.MethodPost, messagesPath, "{}", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"compressed", http.MethodPost, messagesPath, "{}", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 415},
		{"malformed", http.MethodPost, messagesPath, "{", nil, 400},
		{"array", http.MethodPost, messagesPath, "[]", nil, 400},
		{"absent model", http.MethodPost, messagesPath, "{}", nil, 400},
		{"empty model", http.MethodPost, messagesPath, `{"model":""}`, nil, 400},
		{"numeric model", http.MethodPost, messagesPath, `{"model":1}`, nil, 400},
		{"duplicate model", http.MethodPost, messagesPath, `{"model":"exact-model","model":"exact-model"}`, nil, 400},
		{"case conflict", http.MethodPost, messagesPath, `{"model":"exact-model","Model":"other"}`, nil, 400},
		{"case only", http.MethodPost, messagesPath, `{"Model":"exact-model"}`, nil, 400},
		{"trailing", http.MethodPost, messagesPath, `{"model":"exact-model"} {}`, nil, 400},
		{"unknown", http.MethodPost, messagesPath, `{"model":"unknown"}`, nil, 404},
		{"hint", http.MethodPost, messagesPath, `{"model":"exact-model[1m]"}`, nil, 404},
		{"no counting", http.MethodPost, countTokensPath, `{"model":"exact-model"}`, nil, 404},
		{"missing oauth", http.MethodPost, messagesPath, `{"model":"exact-model"}`, func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"duplicate oauth", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Add("Authorization", "Bearer other") }, 400},
		{"duplicate api key", http.MethodGet, modelsPath, "", func(r *http.Request) { r.Header.Add("X-Api-Key", "a"); r.Header.Add("X-Api-Key", "b") }, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request(tc.method, tc.path, tc.body)
			if tc.edit != nil {
				tc.edit(req)
			}
			out := httptest.NewRecorder()
			server.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", out.Code, tc.status, out.Body.String())
			}
			if !strings.Contains(out.Body.String(), `"type":"error"`) {
				t.Fatal("missing native error envelope")
			}
			if strings.Contains(out.Body.String(), "dummy-") {
				t.Fatal("credential echoed")
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	config := testConfig(t)
	config.models["aaa-local"] = model{upstream: config.models[testModel].upstream, displayName: "Local display"}
	out := httptest.NewRecorder()
	newTestRelay(t, config).ServeHTTP(out, request(http.MethodGet, "/v1/models?limit=1000", ""))
	if out.Code != 200 {
		t.Fatalf("status=%d", out.Code)
	}
	want := `{"data":[{"id":"aaa-local","display_name":"Local display"},{"id":"exact-model","display_name":"exact-model"}],"has_more":false}`
	if strings.TrimSpace(out.Body.String()) != want || out.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog=%s", out.Body.String())
	}
}
