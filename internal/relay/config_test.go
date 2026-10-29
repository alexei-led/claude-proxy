package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, name, value string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func configFixture(t *testing.T, edit func(string) string) string {
	t.Helper()
	key := writeFixture(t, "access", "dummy-personal-access\n", 0o600)
	body := `{"version":1,"profile":"personal","listen":"127.0.0.1:43171","access_token_file":"ACCESS","upstreams":{"native":{"messages_url":"http://127.0.0.1:12345/native/messages","auth":{"mode":"oauth_passthrough"}}},"models":{"exact-model":{"upstream":"native"}}}`
	body = strings.ReplaceAll(body, "ACCESS", key)
	if edit != nil {
		body = edit(body)
	}
	return writeFixture(t, "config.json", body, 0o600)
}

func TestConfig(t *testing.T) {
	cases := []struct {
		name, old, replacement string
		valid                  bool
	}{
		{"valid", "", "", true},
		{"unknown", versionFragment, `"unknown":1,"version":1`, false},
		{"case", `"version"`, `"Version"`, false},
		{"duplicate", versionFragment, `"version":1,"version":1`, false},
		{"duplicate nested", oauthFragment, `"mode":"none","mode":"oauth_passthrough"`, false},
		{"version", versionFragment, `"version":2`, false},
		{"profile", personalProfile, "other", false},
		{"remote listener", testHost, "0.0.0.0:43171", false},
		{"hostname listener", testHost, "localhost:43171", false},
		{"zero port", testHost, "127.0.0.1:0", false},
		{"http remote", "http://127.0.0.1:12345/native/messages", "http://example.invalid/v1/messages", false},
		{"url query", "/native/messages", "/native/messages?secret=bad", false},
		{"url userinfo", "http://127.0.0.1", "http://user@127.0.0.1", false},
		{"missing reference", `"upstream":"native"`, `"upstream":"missing"`, false},
		{"empty id", "exact-model", "", false},
		{"invalid UTF8", "exact-model", "exact-\xff", false},
		{"duplicate model id", `"exact-model":{"upstream":"native"}`, `"exact-model":{"upstream":"native"},"exact-model":{"upstream":"native"}`, false},
		{"model field case", `"upstream":"native"`, `"Upstream":"native"`, false},
		{"mode", "oauth_passthrough", "magic", false},
		{"inline secret", oauthFragment, `"mode":"bearer","key":"bad"`, false},
		{"contradiction", oauthFragment, `"mode":"oauth_passthrough","key_file":"unused"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := configFixture(t, func(body string) string {
				if tc.old == "" {
					return body
				}
				return strings.Replace(body, tc.old, tc.replacement, 1)
			})
			_, err := LoadConfig(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
