package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelLookup(t *testing.T) {
	path := configFixture(t, func(body string) string {
		return strings.Replace(body, oauthFragment, `"mode":"bearer","key_file":"provider-secret"`, 1)
	})
	secretPath := filepath.Join(filepath.Dir(path), "provider-secret")
	if err := os.WriteFile(secretPath, []byte(testServerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(secretPath); err != nil {
		t.Fatal(err)
	}
	entry, ok := config.models[testModel]
	if !ok || entry.upstream.key != testServerKey || entry.upstream.mode != authBearer {
		t.Fatal("route or key snapshot lost")
	}
	if _, ok = config.models[testModel+"[1m]"]; ok {
		t.Fatal("model hints normalized")
	}
	if config.Profile() != personalProfile || config.Listen() != testHost {
		t.Fatal("profile boundary changed")
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("missing referenced secret accepted")
	}
}

func TestConfigJSONFraming(t *testing.T) {
	for _, suffix := range []string{"{}", " true", "\n{"} {
		path := configFixture(t, func(body string) string { return body + suffix })
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("accepted multiple JSON values")
		}
	}
}
