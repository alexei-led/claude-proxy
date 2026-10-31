package relay

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func documentedBash(t *testing.T, marker string) string {
	t.Helper()
	body, err := os.ReadFile("../../docs/integration.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)"+strings.Repeat("`", 3)+"bash\n(.*?)\n"+strings.Repeat("`", 3)).FindAllStringSubmatch(string(body), -1)
	for _, block := range blocks {
		if strings.Contains(block[1], marker) {
			return block[1]
		}
	}
	t.Fatal("documented command not found")
	return ""
}

func snippetFixture(t *testing.T, profile string) (string, *os.Root, []string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "claude-proxy")
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{personalProfile, workProfile} {
		if err := os.WriteFile(filepath.Join(dir, "secrets", name+"-access.key"), []byte("dummy-"+name+"-access\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	examples, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + base, "XDG_CONFIG_HOME=" + base, "CLAUDE_CONFIG_DIR=" + filepath.Join(base, "claude-"+profile), "proxy_profile=" + profile, "TMPDIR=" + dir, "examples_dir=" + examples}
	return dir, root, environment
}

func runSnippet(t *testing.T, script string, environment []string) ([]byte, error) {
	t.Helper()
	// #nosec G204 -- Repository-owned examples, fake network commands, isolated fixture environment.
	command := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", script)
	command.Env = environment
	command.Dir = filepath.Join("..", "..")
	return command.CombinedOutput()
}

func TestDocumentedLaunch(t *testing.T) {
	selection := documentedBash(t, `case "$proxy_profile"`)
	script := documentedBash(t, "claude --model")
	for _, profile := range []string{personalProfile, workProfile} {
		for _, tc := range []struct {
			name, headers string
			allowed       bool
		}{
			{"empty", "", true},
			{"unrelated", "X-Company-Context: synthetic\nX-Feature: on", true},
			{"reserved relay", "x-claude-proxy-key: stale", false},
			{"reserved oauth", "Authorization: Bearer dummy", false},
			{"reserved API", " X-API-KEY : dummy", false},
			{"reserved cookie", "Cookie: dummy", false},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				_, root, environment := snippetFixture(t, profile)
				prelude := `claude() {
 test "$#" = 2 && test "$1" = --model && test "$2" = claude-opus-5-5 || return 1
 test -z "${ANTHROPIC_API_KEY+x}" && test -z "${ANTHROPIC_AUTH_TOKEN+x}" || return 1
 printf '%s\000%s\000%s' "$ANTHROPIC_BASE_URL" "$CLAUDE_ROUTER_PROVIDER" "$ANTHROPIC_CUSTOM_HEADERS" > "$config_root/observed"
}
`
				environment = append(environment, "ANTHROPIC_CUSTOM_HEADERS="+tc.headers, "ANTHROPIC_API_KEY=dummy-inherited-api", "ANTHROPIC_AUTH_TOKEN=dummy-inherited-auth")
				output, err := runSnippet(t, prelude+selection+"\n"+script, environment)
				if (err == nil) != tc.allowed {
					t.Fatalf("allowed=%v error=%v output=%s", tc.allowed, err, output)
				}
				observed, readErr := root.ReadFile("observed")
				if !tc.allowed {
					if !os.IsNotExist(readErr) {
						t.Fatal("rejected launch invoked Claude")
					}
					return
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
				parts := strings.Split(string(observed), "\x00")
				wantHeaders := localKeyHeader + ": dummy-" + profile + "-access"
				if tc.headers != "" {
					wantHeaders = tc.headers + "\n" + wantHeaders
				}
				wantURL := "http://" + exampleConfig(t, profile+".json").Listen
				if len(parts) != 3 || parts[0] != wantURL || parts[1] != "proxy" || parts[2] != wantHeaders {
					t.Fatal("documented launch lost headers or mixed profile settings")
				}
			})
		}
	}
}

func TestDocumentedProfileSelection(t *testing.T) {
	selection := documentedBash(t, `case "$proxy_profile"`)
	for _, profile := range []string{"", "../outside", "unsupported-profile"} {
		t.Run(profile, func(t *testing.T) {
			_, _, environment := snippetFixture(t, profile)
			if _, err := runSnippet(t, selection, environment); err == nil {
				t.Fatal("unsafe or missing profile accepted")
			}
		})
	}
}

func TestDocumentedSetup(t *testing.T) {
	const (
		existingConfig  = "config"
		existingAbsent  = "absent"
		existingRouter  = "router"
		existingSymlink = "symlink"
	)
	selection := documentedBash(t, `case "$proxy_profile"`)
	script := documentedBash(t, `cp -n "$examples_dir/$proxy_profile.json"`)
	for _, profile := range []string{personalProfile, workProfile} {
		for _, existing := range []string{existingAbsent, existingConfig, existingRouter, existingSymlink} {
			t.Run(profile+"/"+existing, func(t *testing.T) {
				dir, root, environment := snippetFixture(t, profile)
				claudeDir := filepath.Join(filepath.Dir(dir), "claude-"+profile)
				if err := os.Mkdir(claudeDir, 0o700); err != nil {
					t.Fatal(err)
				}
				routerRoot, err := os.OpenRoot(claudeDir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = routerRoot.Close() }()
				const sentinel = "existing configuration"
				switch existing {
				case existingConfig:
					if err = os.WriteFile(filepath.Join(dir, profile+".json"), []byte(sentinel), 0o600); err != nil {
						t.Fatal(err)
					}
				case existingRouter:
					if err = os.WriteFile(filepath.Join(claudeDir, routerExample), []byte(sentinel), 0o600); err != nil {
						t.Fatal(err)
					}
				case existingSymlink:
					if err = os.Symlink("missing-target", filepath.Join(dir, profile+".json")); err != nil {
						t.Fatal(err)
					}
				}
				output, err := runSnippet(t, selection+"\n"+script, environment)
				if (err == nil) != (existing == existingAbsent) {
					t.Fatalf("setup existing=%s: %v %s", existing, err, output)
				}
				config, configErr := root.ReadFile(profile + ".json")
				router, routerErr := routerRoot.ReadFile(routerExample)
				switch existing {
				case existingAbsent:
					if configErr != nil || routerErr != nil || !bytes.Equal(config, exampleBytes(t, profile+".json")) || !bytes.Equal(router, exampleBytes(t, routerExample)) {
						t.Fatal("selected example was not copied")
					}
				case existingConfig:
					if configErr != nil || string(config) != sentinel || !os.IsNotExist(routerErr) {
						t.Fatal("setup overwrote existing proxy configuration")
					}
				case existingRouter:
					if routerErr != nil || string(router) != sentinel || !os.IsNotExist(configErr) {
						t.Fatal("setup overwrote existing router configuration")
					}
				case existingSymlink:
					info, statErr := root.Lstat(profile + ".json")
					if statErr != nil || info.Mode()&os.ModeSymlink == 0 || !os.IsNotExist(routerErr) {
						t.Fatal("setup followed or replaced a dangling symlink")
					}
				}
			})
		}
	}
}

func TestDocumentedCatalogCheck(t *testing.T) {
	selection := documentedBash(t, `case "$proxy_profile"`)
	script := documentedBash(t, "curl --noproxy")
	for _, profile := range []string{personalProfile, workProfile} {
		for _, status := range []string{"200", "302", "401"} {
			t.Run(profile+"/"+status, func(t *testing.T) {
				dir, root, environment := snippetFixture(t, profile)
				prelude := `curl() {
 printf '%s\000' "$@" > "$config_root/curl-arguments"
 cat "$header_file" > "$config_root/captured-header"
 printf '%s' "$header_file" > "$config_root/header-path"
 printf '%s\n' '{"data":[]}' > "$catalog_file"
 printf '%s' "$FAKE_CATALOG_STATUS"
}
`
				environment = append(environment, "FAKE_CATALOG_STATUS="+status)
				output, err := runSnippet(t, prelude+selection+"\n"+script, environment)
				if (err == nil) != (status == "200") {
					t.Fatalf("catalog command status=%s: %v: %s", status, err, output)
				}
				arguments, err := root.ReadFile("curl-arguments")
				if err != nil {
					t.Fatal(err)
				}
				token := "dummy-" + profile + "-access"
				if strings.Contains(string(arguments), token) {
					t.Fatal("local token exposed in curl arguments")
				}
				wantURL := "http://" + exampleConfig(t, profile+".json").Listen
				for _, expected := range []string{"--noproxy\x00*\x00", "--max-time\x005\x00", wantURL + "/v1/models?limit=1000"} {
					if !strings.Contains(string(arguments), expected) {
						t.Fatal("catalog command lost profile or local bounds")
					}
				}
				header, err := root.ReadFile("captured-header")
				if err != nil {
					t.Fatal(err)
				}
				if string(header) != localKeyHeader+": "+token+"\n" {
					t.Fatal("catalog header changed or mixed profiles")
				}
				temporary, err := root.ReadFile("header-path")
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(dir, string(temporary))
				if err != nil || !filepath.IsLocal(relative) {
					t.Fatal("temporary header escaped the fixture directory")
				}
				if _, err = root.Stat(relative); !os.IsNotExist(err) {
					t.Fatal("temporary credential header was not removed")
				}
			})
		}
	}
}
