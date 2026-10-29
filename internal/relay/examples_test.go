package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	personalExample = "personal.json"
	workExample     = "work.json"
	routerExample   = "proxy-router.json"
)

type routerProfile struct {
	Models map[string]struct{ ID string }
	Routes map[string]struct{ Model string }
}

func exampleBytes(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	body, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func exampleConfig(t *testing.T, name string) fileConfig {
	t.Helper()
	body := exampleBytes(t, name)
	if err := validateConfigJSON(body); err != nil {
		t.Fatal(err)
	}
	var config fileConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func mixedProfile(t *testing.T) routerProfile {
	t.Helper()
	var profile routerProfile
	if err := json.Unmarshal(exampleBytes(t, routerExample), &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.Models) == 0 || len(profile.Routes) != 4 {
		t.Fatal("mixed profile needs models and four tier routes")
	}
	return profile
}

func materializeConfig(t *testing.T, raw fileConfig) *Config {
	t.Helper()
	dir := t.TempDir()
	secrets := []string{raw.AccessTokenFile}
	for _, entry := range raw.Upstreams {
		if entry.Auth.KeyFile != "" {
			secrets = append(secrets, entry.Auth.KeyFile)
		}
	}
	for _, name := range secrets {
		if !filepath.IsLocal(name) || !strings.HasPrefix(name, "secrets/") {
			t.Fatal("example secret path must stay inside its private directory")
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("dummy-"+raw.Profile+"-"+filepath.Base(name)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestExampleConfigs(t *testing.T) {
	profile := mixedProfile(t)
	secretOwners := make(map[string]string)
	for _, name := range []string{personalExample, workExample} {
		t.Run(name, func(t *testing.T) {
			raw := exampleConfig(t, name)
			config := materializeConfig(t, raw)
			paths := []string{raw.AccessTokenFile}
			for _, entry := range raw.Upstreams {
				if entry.Auth.KeyFile != "" {
					paths = append(paths, entry.Auth.KeyFile)
				}
			}
			for _, path := range paths {
				if owner, exists := secretOwners[path]; exists && owner != raw.Profile {
					t.Fatal("example profiles share a credential path")
				}
				secretOwners[path] = raw.Profile
			}
			for alias, entry := range profile.Models {
				if _, ok := config.models[entry.ID]; !ok {
					t.Fatalf("router model %s is absent from %s catalog", alias, name)
				}
				if strings.Contains(entry.ID, "[1m]") {
					t.Fatal("examples must use exact native IDs without context hints")
				}
			}
			for tier, route := range profile.Routes {
				if _, ok := profile.Models[route.Model]; !ok {
					t.Fatalf("tier %s references missing model", tier)
				}
			}
			recorder := httptest.NewRecorder()
			req := request(http.MethodGet, modelsPath+"?limit=1000", "")
			req.Host = config.listen
			req.Header.Set(localKeyHeader, config.token)
			server := newTestRelay(t, config)
			defer server.transport.CloseIdleConnections()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("catalog status=%d", recorder.Code)
			}
			var catalog struct{ Data []catalogModel }
			if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(catalog.Data))
			for _, entry := range catalog.Data {
				ids = append(ids, entry.ID)
			}
			expected := make([]string, 0, len(raw.Models))
			for id := range raw.Models {
				expected = append(expected, id)
			}
			slices.Sort(expected)
			if !slices.Equal(ids, expected) {
				t.Fatal("catalog IDs differ from configured IDs")
			}
			for _, entry := range profile.Models {
				if !slices.Contains(ids, entry.ID) {
					t.Fatal("router ID is absent from the returned catalog")
				}
			}
		})
	}
}
