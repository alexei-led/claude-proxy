package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConfig(t *testing.T) {
	path := configFixture(t, func(body string) string {
		return strings.ReplaceAll(body, oauthFragment, `"mode":"bearer","key_file":"missing-provider-key"`)
	})
	client, err := LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL != "http://"+testHost || client.LocalKey != "dummy-personal-access" || len(client.Models) != 1 || client.Models[0] != testModel {
		t.Fatal("unexpected connection")
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("server should require its own provider key")
	}
}

func TestClientConfigValidation(t *testing.T) {
	for _, edit := range []func(string) string{
		func(s string) string { return strings.ReplaceAll(s, testHost, "example.invalid:443") },
		func(s string) string { return strings.ReplaceAll(s, `"upstream":"native"`, `"upstream":"missing"`) },
		func(s string) string { return strings.ReplaceAll(s, testModel, "") },
		func(s string) string { return strings.ReplaceAll(s, versionFragment, `"version":2`) },
		func(s string) string { return strings.ReplaceAll(s, versionFragment, `"version":1,"version":1`) },
	} {
		if _, err := LoadClientConfig(configFixture(t, edit)); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
}

func TestClientConfigRequiresPrivateLocalToken(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("dummy-access"), 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- Deliberately unsafe permissions exercise token rejection.
	if err := os.Chmod(token, 0o644); err != nil {
		t.Fatal(err)
	}
	path := configFixture(t, func(s string) string {
		start := strings.Index(s, `"access_token_file":"`) + len(`"access_token_file":"`)
		end := strings.Index(s[start:], `"`) + start
		return s[:start] + token + s[end:]
	})
	if _, err := LoadClientConfig(path); err == nil {
		t.Fatal("public local token accepted")
	}
	if err := os.Chmod(token, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientConfig(path); err != nil {
		t.Fatal(err)
	}
}
