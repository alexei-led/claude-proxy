package managed

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

func manualFixture(t *testing.T) (string, *relay.Config) {
	t.Helper()
	path, _ := fixture(t, "100ms")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	// #nosec G304 -- Read the current test's own private fixture.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- Assign a reserved local test port to the test-owned fixture.
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(body), "127.0.0.1:43171", address)), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := relay.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, config
}

func startManual(t *testing.T, binary, path string, config *relay.Config) *exec.Cmd {
	t.Helper()
	// #nosec G204 -- Execute only this test-built proxy with its private fixture.
	command := exec.CommandContext(t.Context(), binary, "--config", path)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+config.Listen()+"/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Claude-Proxy-Key", "dummy-local-key")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return command
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manual listener did not become ready")
	return nil
}

func TestManagedRunReusesManualServer(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	path, config := manualFixture(t)
	manual := startManual(t, binary, path, config)
	info, err := cliStatus(t, binary, path)
	if err != nil {
		t.Fatal("manual server is not discoverable:", err)
	}
	if info.Mode != ModeManual {
		t.Fatal("manual ownership was not reported")
	}
	client := startClient(t, binary, path)
	connection := clientConnection(t, client)
	if connection[0] != "http://"+config.Listen() {
		t.Fatal("managed client did not reuse manual listener")
	}
	state, err := cliStatus(t, binary, path)
	if err != nil || state.Mode != ModeManual || state.Sessions != 1 || state.PID != manual.Process.Pid || info.PID != state.PID {
		t.Fatal("run started a duplicate broker")
	}
	// #nosec G204 -- A repeated manual start may only report this existing test server.
	duplicate := exec.CommandContext(t.Context(), binary, "--config", path)
	if output, err := duplicate.CombinedOutput(); err != nil {
		t.Fatalf("duplicate foreground start: %v %s", err, output)
	}
	_ = client.input.Close()
	_ = client.command.Wait()
	_ = manual.Process.Signal(os.Interrupt)
	_ = manual.Wait()
}

func TestSecondManualStartReusesExistingManagedBroker(t *testing.T) {
	privateRuntime(t)
	binary := buildCLI(t)
	path, _ := fixture(t, "100ms")
	client := startClient(t, binary, path)
	connection := clientConnection(t, client)
	before, err := cliStatus(t, binary, path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- A second foreground invocation uses only this test's executable/config.
	command := exec.CommandContext(t.Context(), binary, "--config", path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("existing broker not reused: %v %s", err, output)
	}
	after, err := cliStatus(t, binary, path)
	if err != nil || after.Mode != ModeManaged || after.PID != before.PID || "http://"+after.Address != connection[0] {
		t.Fatal("foreground invocation duplicated or replaced the broker")
	}
	_ = client.input.Close()
	_ = client.command.Wait()
}

func TestManualModeNeverUsesIdleTimeout(t *testing.T) {
	_, config := fixture(t, "1ms")
	state := &broker{leases: make(map[int]lease), server: relay.NewServer(config, nil), info: Info{Mode: ModeManual}}
	now := time.Now()
	if state.tick(now, time.Millisecond) || state.tick(now.Add(time.Hour), time.Millisecond) {
		t.Fatal("manual server auto-closed")
	}
}

func TestForegroundBindWaitsForRetiringListener(t *testing.T) {
	calls := 0
	bind := func(ctx context.Context, network, address string) (net.Listener, error) {
		calls++
		if calls < 3 {
			return nil, &net.OpError{Op: "listen", Net: network, Err: syscall.EADDRINUSE}
		}
		return (&net.ListenConfig{}).Listen(ctx, network, address)
	}
	listener, err := listenForeground(t.Context(), "127.0.0.1:0", bind)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if calls != 3 {
		t.Fatal("retiring listener was not retried")
	}
}

func TestForegroundBindRefusesOtherErrorsAndHonorsCancellation(t *testing.T) {
	calls := 0
	denied := func(context.Context, string, string) (net.Listener, error) { calls++; return nil, syscall.EACCES }
	if listener, err := listenForeground(t.Context(), "127.0.0.1:1", denied); err == nil {
		_ = listener.Close()
		t.Fatal("bind permission failure accepted")
	}
	if calls != 1 {
		t.Fatal("non-conflict bind failure retried")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	busy := func(context.Context, string, string) (net.Listener, error) { return nil, syscall.EADDRINUSE }
	if listener, err := listenForeground(ctx, "127.0.0.1:1", busy); err == nil {
		_ = listener.Close()
		t.Fatal("canceled bind accepted")
	}
}
