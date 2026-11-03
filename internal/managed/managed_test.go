package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

func fixture(t *testing.T, idle string) (string, *relay.Config) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "access"), []byte("dummy-local-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profile.json")
	body := fmt.Sprintf(`{"version":1,"profile":"personal","listen":"127.0.0.1:43171","access_token_file":"access","runtime":{"idle_timeout":%q,"max_inflight":32},"upstreams":{"native":{"messages_url":"http://127.0.0.1:1/messages","auth":{"mode":"none"}}},"models":{"native-model":{"upstream":"native"}}}`, idle)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := relay.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, config
}

func privateRuntime(t *testing.T) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "cp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_RUNTIME_DIR", root)
}

func TestIdleLifecycle(t *testing.T) {
	_, config := fixture(t, "1m")
	makeBroker := func() *broker {
		return &broker{leases: make(map[int]lease), server: relay.NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	}
	now := time.Now()
	b := makeBroker()
	if b.tick(now, time.Minute) || b.tick(now.Add(59*time.Second), time.Minute) {
		t.Fatal("closed before idle timeout")
	}
	if !b.tick(now.Add(time.Minute), time.Minute) {
		t.Fatal("did not close after zero-session idle window")
	}
	if !b.closing {
		t.Fatal("new registrations not blocked during shutdown")
	}
	b = makeBroker()
	b.leases[123] = lease{}
	if b.tick(now, time.Second) || b.tick(now.Add(time.Hour), time.Second) {
		t.Fatal("uncertain process tracking must prevent shutdown")
	}
	b = makeBroker()
	if b.tick(now, 0) || b.tick(now.Add(time.Hour), 0) {
		t.Fatal("disabled idle timeout closed broker")
	}
	b = makeBroker()
	_ = b.tick(now, time.Minute)
	b.leases[123] = lease{}
	_ = b.tick(now.Add(50*time.Second), time.Minute)
	delete(b.leases, 123)
	if b.tick(now.Add(time.Minute), time.Minute) || b.tick(now.Add(110*time.Second), time.Minute) {
		t.Fatal("new client did not reset idle window")
	}
	if !b.tick(now.Add(2*time.Minute), time.Minute) {
		t.Fatal("reset idle window did not expire")
	}
}

func TestProcessWatch(t *testing.T) {
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", "read line")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	watch, err := watchProcess(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	defer watch.close()
	if alive, err := watch.alive(); err != nil || !alive {
		t.Fatal("live process not tracked")
	}
	_ = input.Close()
	_ = command.Wait()
	alive, err := watch.alive()
	if err != nil || alive {
		t.Fatalf("exited process still alive: %v", err)
	}
}

func TestActiveRequestPreventsIdleDrain(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { close(entered); <-release; w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	path, _ := fixture(t, "1ms")
	// #nosec G304 -- Read this test-owned private configuration fixture.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), "http://127.0.0.1:1/messages", upstream.URL+"/messages"))
	// #nosec G703 -- Rewrite only the private configuration created by this test.
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := relay.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := relay.NewServer(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	local := httptest.NewServer(server)
	defer local.Close()
	b := &broker{leases: make(map[int]lease), server: server}
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, local.URL+"/v1/messages", strings.NewReader(`{"model":"native-model"}`))
		if err != nil {
			done <- err
			return
		}
		req.Host = config.Listen()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Claude-Proxy-Key", "dummy-local-key")
		response, err := local.Client().Do(req)
		if err == nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	<-entered
	now := time.Now()
	if b.tick(now, time.Millisecond) || b.tick(now.Add(time.Hour), time.Millisecond) {
		t.Fatal("active request was considered idle")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if server.ActiveRequests() != 0 {
		t.Fatal("forwarding slot was not released")
	}
	if b.tick(now.Add(2*time.Hour), time.Millisecond) {
		t.Fatal("idle window must begin after request completion")
	}
	if !b.tick(now.Add(3*time.Hour), time.Millisecond) {
		t.Fatal("idle drain did not become available")
	}
}

func TestGenerationAndRuntimeSafety(t *testing.T) {
	privateRuntime(t)
	path, config := fixture(t, "5m")
	first, err := locate(path, config)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := locate(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if first.identity != repeated.identity {
		t.Fatal("same snapshot did not reuse generation")
	}
	// #nosec G304 -- Read this test-owned private configuration fixture.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- Mutate only the current test-owned configuration fixture.
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(body), "native-model", "other-model")), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := relay.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	next, err := locate(path, changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.identity == next.identity {
		t.Fatal("configuration change reused old generation")
	}
	// #nosec G302 -- Deliberately unsafe directory permissions exercise runtime rejection.
	if err := os.Chmod(first.directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := locate(path, changed); err == nil {
		t.Fatal("unsafe runtime permissions accepted")
	}
}

func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "claude-proxy")
	// #nosec G204 -- Build only this repository's command into a test-owned directory.
	command := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/claude-proxy")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	return binary
}

type testClient struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  string
}

func startClient(t *testing.T, binary, path string) testClient {
	t.Helper()
	output := filepath.Join(t.TempDir(), "connection")
	script := "printf '%s\\n' \"$ANTHROPIC_BASE_URL\" \"$$\" > \"$1\"; read line"
	// #nosec G204 -- Run the test-built CLI and a fixed dummy client without external endpoints.
	command := exec.CommandContext(t.Context(), binary, "run", "--config", path, "--", "/bin/sh", "-c", script, "dummy-client", output)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return testClient{command: command, input: input, output: output}
}

func clientConnection(t *testing.T, client testClient) []string {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		// #nosec G304 -- Read only this test's dummy-client connection capture.
		body, err := os.ReadFile(client.output)
		if err == nil {
			return strings.Split(strings.TrimSpace(string(body)), "\n")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed client did not become ready")
	return nil
}

func cliStatus(t *testing.T, binary, path string) (Info, error) {
	t.Helper()
	// #nosec G204 -- Query the test-built CLI's private broker using the fixture config.
	command := exec.CommandContext(t.Context(), binary, "status", "--config", path)
	body, err := command.Output()
	if err != nil {
		return Info{}, err
	}
	var info Info
	err = json.Unmarshal(body, &info)
	return info, err
}

func TestManagedClientsShareAndAutoClose(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	path, _ := fixture(t, "100ms")
	const count = 24
	clients := make([]testClient, count)
	for index := range clients {
		clients[index] = startClient(t, binary, path)
	}
	connections := make([][]string, len(clients))
	for index, client := range clients {
		connections[index] = clientConnection(t, client)
	}
	for index, connection := range connections {
		if len(connection) != 2 || connection[0] != connections[0][0] || connection[1] != strconv.Itoa(clients[index].command.Process.Pid) {
			t.Fatal("clients did not share broker or preserve exec PID")
		}
	}
	info, err := cliStatus(t, binary, path)
	if err != nil || info.Sessions != count {
		t.Fatalf("registered sessions: %+v err=%v", info, err)
	}
	_ = clients[0].input.Close()
	_ = clients[0].command.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := cliStatus(t, binary, path)
		if err != nil || current.PID != info.PID {
			t.Fatalf("broker closed with clients alive: %+v %v", current, err)
		}
		if current.Sessions == count-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exited client registration was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = clients[1].command.Process.Kill()
	_ = clients[1].command.Wait()
	for _, client := range clients[2:] {
		_ = client.input.Close()
		_ = client.command.Wait()
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := cliStatus(t, binary, path); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("broker did not close after all clients exited")
}

func TestBusyBrokerCannotStop(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	path, _ := fixture(t, "100ms")
	client := startClient(t, binary, path)
	_ = clientConnection(t, client)
	// #nosec G204 -- A stop request against this test's own registered client.
	stop := exec.CommandContext(t.Context(), binary, "stop", "--config", path)
	if err := stop.Run(); err == nil {
		t.Fatal("active broker was stopped")
	}
	_ = client.input.Close()
	_ = client.command.Wait()
}

func TestCanceledLockWait(t *testing.T) {
	privateRuntime(t)
	path, config := fixture(t, "5m")
	loc, err := locate(path, config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := acquire(t.Context(), loc.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if second, err := acquire(ctx, loc.lock); err == nil {
		_ = second.Close()
		t.Fatal("canceled lock wait succeeded")
	}
}
