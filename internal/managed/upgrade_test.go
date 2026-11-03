package managed

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInstalledBinaryUpgradeStartsNewGeneration(t *testing.T) {
	privateRuntime(t)
	oldBinary := buildCLI(t)
	path, _ := fixture(t, "100ms")
	oldClient := startClient(t, oldBinary, path)
	oldConnection := clientConnection(t, oldClient)
	oldInfo, err := cliStatus(t, oldBinary, path)
	if err != nil {
		t.Fatal(err)
	}
	nextBinary := filepath.Join(t.TempDir(), "claude-proxy")
	// #nosec G204 -- A different test build ID simulates an installed binary replacement.
	build := exec.CommandContext(t.Context(), "go", "build", "-ldflags=-buildid=managed-upgrade-test", "-o", nextBinary, "../../cmd/claude-proxy")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("replacement build: %v\n%s", err, output)
	}
	nextClient := startClient(t, nextBinary, path)
	nextConnection := clientConnection(t, nextClient)
	nextInfo, err := cliStatus(t, nextBinary, path)
	if err != nil {
		t.Fatal(err)
	}
	if oldInfo.Identity == nextInfo.Identity || oldInfo.PID == nextInfo.PID || oldConnection[0] == nextConnection[0] {
		t.Fatal("binary replacement reused old running generation")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, oldConnection[0]+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Claude-Proxy-Key", "dummy-local-key")
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("installed upgrade interrupted old client")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("old generation stopped before its client exited")
	}
	_ = oldClient.input.Close()
	_ = oldClient.command.Wait()
	_ = nextClient.input.Close()
	_ = nextClient.command.Wait()
}
