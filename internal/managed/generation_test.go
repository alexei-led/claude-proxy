package managed

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

func TestConfigurationRolloverKeepsExistingClients(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	path, _ := fixture(t, "100ms")
	first := startClient(t, binary, path)
	firstConnection := clientConnection(t, first)
	old, err := cliStatus(t, binary, path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- This is the private configuration fixture owned by the test.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- Change only a test-owned routing display label.
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), `"upstream":"native"`, `"upstream":"native","display_name":"new generation"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	second := startClient(t, binary, path)
	secondConnection := clientConnection(t, second)
	current, err := cliStatus(t, binary, path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Identity == old.Identity || current.PID == old.PID || firstConnection[0] == secondConnection[0] {
		t.Fatal("new configuration reused the running old generation")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, firstConnection[0]+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Claude-Proxy-Key", "dummy-local-key")
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("old session was interrupted during rollover")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("old generation stopped accepting its registered client")
	}

	_ = first.input.Close()
	_ = first.command.Wait()
	_ = second.input.Close()
	_ = second.command.Wait()
}

func TestSeparateConfigurationsNeverShareBroker(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	personalPath, _ := fixture(t, "100ms")
	workPath, _ := fixture(t, "100ms")
	// #nosec G304 -- Read this test-owned profile.
	body, err := os.ReadFile(workPath)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- Work profile remains inside the test-owned directory.
	if err := os.WriteFile(workPath, []byte(strings.ReplaceAll(string(body), `"personal"`, `"work"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	personal := startClient(t, binary, personalPath)
	work := startClient(t, binary, workPath)
	p := clientConnection(t, personal)
	w := clientConnection(t, work)
	if p[0] == w[0] {
		t.Fatal("personal and work configurations shared a listener")
	}
	_ = personal.input.Close()
	_ = personal.command.Wait()
	_ = work.input.Close()
	_ = work.command.Wait()
}

func TestRuntimeSocketAndLockSafety(t *testing.T) {
	privateRuntime(t)
	path, config := fixture(t, "5m")
	loc, err := locate(path, config)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "preserved")
	if err := os.WriteFile(target, []byte("do not alter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, loc.lock); err != nil {
		t.Fatal(err)
	}
	if file, err := acquire(t.Context(), loc.lock); err == nil {
		_ = file.Close()
		t.Fatal("symlink lock was accepted")
	}
	if err := os.WriteFile(loc.socket, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(loc.socket); err == nil {
		t.Fatal("non-socket stale file was removed")
	}
}

func TestManagedLogRemainsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	file, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	sink := &boundedLog{file: file}
	for _, size := range []int{maxLogBytes - 1, 200, maxLogBytes + 100} {
		if written, err := sink.Write([]byte(strings.Repeat("x", size))); err != nil || written != size {
			t.Fatal("bounded log write failed")
		}
		info, err := file.Stat()
		if err != nil || info.Size() > maxLogBytes {
			t.Fatal("managed log exceeded its size limit")
		}
	}
}

func TestIdleWindowResetsWhenAClientRegisters(t *testing.T) {
	_, config := fixture(t, "5m")
	state := &broker{leases: make(map[int]lease), server: relay.NewServer(config, nil)}
	now := time.Now()
	if state.tick(now, time.Second) {
		t.Fatal("closed at beginning of idle window")
	}
	state.leases[os.Getpid()] = lease{}
	if state.tick(now.Add(time.Hour), time.Second) {
		t.Fatal("uncertain live registration was closed")
	}
	delete(state.leases, os.Getpid())
	if state.tick(now.Add(2*time.Hour), time.Second) {
		t.Fatal("idle timer was not reset by registration")
	}
}
