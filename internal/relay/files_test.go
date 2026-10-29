package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecret(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
		valid      bool
	}{
		{"owner read write", testSecret, 0o600, true},
		{"owner read", "dummy-key\r\n", 0o400, true},
		{"newline", "dummy-key\n", 0o600, true},
		{"group read", testSecret, 0o640, false},
		{"world read", testSecret, 0o604, false},
		{"executable", testSecret, 0o700, false},
		{"empty", "", 0o600, false},
		{"control", "dummy\nkey", 0o600, false},
		{"double ending", "dummy-key\n\n", 0o600, false},
		{"bare CR", "dummy-key\r", 0o600, false},
		{"oversize", strings.Repeat("x", maxSecretBytes+1), 0o600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, "key", tc.body, tc.mode)
			_, err := loadSecret("unused", path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "dummy") {
				t.Fatal("secret in error")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		target := writeFixture(t, "target", testSecret, 0o600)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSecret("unused", link); err == nil {
			t.Fatal("accepted symlink")
		}
	})
	t.Run("directory", func(t *testing.T) {
		if _, err := loadSecret("unused", t.TempDir()); err == nil {
			t.Fatal("accepted directory")
		}
	})
}

func TestConfigFilePermissions(t *testing.T) {
	path := configFixture(t, nil)
	// #nosec G302 -- Deliberately unsafe fixture: configuration must reject it.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("accepted writable config")
	}
}
