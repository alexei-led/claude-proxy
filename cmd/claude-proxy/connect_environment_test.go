package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

const testLocalKey = "dummy-local"
const testNativeModel = "native"
const testClientName = "claude"

func environmentMap(environment []string) map[string]string {
	result := map[string]string{}
	for _, item := range environment {
		if key, value, ok := strings.Cut(item, "="); ok {
			result[key] = value
		}
	}
	return result
}

func TestClientEnvironment(t *testing.T) {
	parent := []string{"HOME=/fixture", "CLAUDE_CONFIG_DIR=/fixture/work", "PATH=/bin", "OTHER=value", "ANTHROPIC_API_KEY=unused-api-key", "ANTHROPIC_AUTH_TOKEN=unused-token", "ANTHROPIC_CUSTOM_HEADERS=X-Unrelated: kept"}
	before := append([]string(nil), parent...)
	config := &relay.ClientConfig{BaseURL: "http://127.0.0.1:43172", LocalKey: testLocalKey}
	result, err := clientEnvironment(parent, config)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(result)
	if values[baseURLEnv] != config.BaseURL || values[routerProviderEnv] != proxyProvider || values[customHeadersEnv] != "X-Unrelated: kept\nX-Claude-Proxy-Key: dummy-local" {
		t.Fatal("connection environment missing")
	}
	for _, key := range []string{"HOME", "CLAUDE_CONFIG_DIR", "PATH", "OTHER"} {
		if values[key] != environmentMap(parent)[key] {
			t.Fatalf("changed %s", key)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := values[key]; ok {
			t.Fatalf("retained %s", key)
		}
	}
	if !reflect.DeepEqual(parent, before) {
		t.Fatal("parent environment changed")
	}
}

func TestClientEnvironmentConflicts(t *testing.T) {
	config := &relay.ClientConfig{BaseURL: "http://127.0.0.1:43172", LocalKey: testLocalKey}
	for _, item := range []string{
		"ANTHROPIC_BASE_URL=https://example.invalid",
		"CLAUDE_CODE_OAUTH_TOKEN=private-override",
		"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=true", "CLAUDE_CODE_USE_FOUNDRY=yes",
		"ANTHROPIC_CUSTOM_HEADERS=Authorization: private-credential",
		"ANTHROPIC_CUSTOM_HEADERS=x-claude-proxy-key: private-credential",
		"ANTHROPIC_CUSTOM_HEADERS=Cookie: private-credential",
	} {
		_, err := clientEnvironment([]string{item}, config)
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe conflict error for %s", strings.SplitN(item, "=", 2)[0])
		}
	}
}

func TestClientHeaders(t *testing.T) {
	for _, headers := range []string{"Bad Header: value", "missing-colon", "X-Test: bad\x00value", "X-Test: bad\rvalue", "X-Test: bad\x7f", "Host: other", "Connection: close", "Content-Length: 0", "Transfer-Encoding: chunked", "X-Api-Key: key", "Proxy-Authorization: key"} {
		if _, err := clientHeaders(headers, testLocalKey); err == nil {
			t.Fatal("unsafe headers accepted")
		}
	}
	value, err := clientHeaders("X-Test: one\r\nX-Other: two\r\n", testLocalKey)
	if err != nil || value != "X-Test: one\nX-Other: two\nX-Claude-Proxy-Key: dummy-local" {
		t.Fatal("CRLF headers not preserved")
	}
}

func TestClientSettingsArguments(t *testing.T) {
	for _, name := range []string{baseURLEnv, customHeadersEnv, "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CONFIG_DIR", routerProviderEnv} {
		value := fmt.Sprintf(`{"env":{%q:"private-value"}}`, name)
		err := checkSettingsArguments([]string{testClientName, settingsFlag, value})
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatal("settings override accepted or leaked")
		}
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"EDITOR":"vim"},"model":"deepseek-flash[1m]"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{testClientName, settingsFlag, path},
		{testClientName, "--settings=" + path},
		{testClientName, settingsFlag, `{"env":{"EDITOR":"vim"}}`},
	} {
		if err := checkSettingsArguments(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{`{`, `{"env":{"EDITOR":true}}`, "missing-settings-file"} {
		if err := checkSettingsArguments([]string{testClientName, "--settings=" + value}); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}

func TestCatalogPreflightFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{}`}, {"unavailable", 503, `{}`}, {"redirect", 302, `{}`},
		{"invalid", 200, `{`}, {"missing", 200, `{"data":[],"has_more":false}`},
		{"paginated", 200, `{"data":[{"id":"native"}],"has_more":true}`},
		{"oversized", 200, strings.Repeat(" ", (4<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(localKeyHeader) != testLocalKey || r.Header.Get("Authorization") != "" {
					t.Error("unexpected catalog authentication")
				}
				w.Header().Set("Location", "https://example.invalid/credential-leak")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := checkCatalog(t.Context(), &relay.ClientConfig{BaseURL: server.URL, LocalKey: testLocalKey, Models: []string{testNativeModel}})
			if err == nil || strings.Contains(err.Error(), testLocalKey) || strings.Contains(err.Error(), server.URL) {
				t.Fatal("unsafe catalog error")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checkCatalog(ctx, &relay.ClientConfig{BaseURL: "http://127.0.0.1:1", LocalKey: testLocalKey}); err == nil {
		t.Fatal("canceled preflight accepted")
	}
}

func TestConnectExecPreservesProcessAndExit(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude-proxy")
	// #nosec G204 -- Build only this package into a test-owned temporary directory.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"native"}],"has_more":false}`)
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(dir, "access"), []byte(testLocalKey), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	config := fmt.Sprintf(`{"version":1,"profile":"work","listen":%q,"access_token_file":"access","upstreams":{"native":{"messages_url":"https://example.invalid/v1/messages","auth":{"mode":"oauth_passthrough"}}},"models":{"native":{"upstream":"native"}}}`, strings.TrimPrefix(server.URL, "http://"))
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "fake-claude")
	capture := filepath.Join(dir, "capture")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_ROUTER_PROVIDER\" \"$ANTHROPIC_CUSTOM_HEADERS\" > \"$1\"\nprintf child-ready\nexit 37\n"
	if err := os.WriteFile(child, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- This dummy client must be executable by its test owner.
	if err := os.Chmod(child, 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Execute only the binary and dummy client built in this test.
	command := exec.CommandContext(t.Context(), binary, "connect", configFlag, path, "--", child, capture)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CLAUDE_CONFIG_DIR=" + dir + "/work"}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("child status: %v; output=%s", err, output)
	}
	if string(output) != "child-ready" {
		t.Fatalf("unexpected output %q", output)
	}
	// #nosec G304 -- Read the capture file created inside this test-owned directory.
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 4 || lines[0] != strconv.Itoa(command.Process.Pid) || lines[1] != dir+"/work" || lines[2] != proxyProvider || lines[3] != "X-Claude-Proxy-Key: dummy-local" {
		t.Fatal("exec did not preserve PID/profile/environment")
	}
}

func TestCatalogPreflightSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": testNativeModel}, {"id": "extra"}}, "has_more": false})
	}))
	defer server.Close()
	if err := checkCatalog(t.Context(), &relay.ClientConfig{BaseURL: server.URL, LocalKey: testLocalKey, Models: []string{testNativeModel}}); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsSpecialFilesAndAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{path, filepath.Dir(path), `{"apiKeyHelper":"private-command"}`} {
		err := checkSettingsArguments([]string{testClientName, settingsFlag, value})
		if err == nil || strings.Contains(err.Error(), "private-command") {
			t.Fatal("unsafe settings accepted or leaked")
		}
	}
}

func TestCatalogDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	err := checkCatalog(t.Context(), &relay.ClientConfig{BaseURL: source.URL, LocalKey: testLocalKey, Models: []string{testNativeModel}})
	if err == nil || redirected.Load() != 0 {
		t.Fatal("catalog redirect followed")
	}
}

func TestCatalogPreflightTimeout(t *testing.T) {
	ended := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(ended) }))
	defer server.Close()
	if err := checkCatalog(t.Context(), &relay.ClientConfig{BaseURL: server.URL, LocalKey: testLocalKey}); err == nil {
		t.Fatal("stalled catalog accepted")
	}
	<-ended
}
