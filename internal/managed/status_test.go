package managed

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestStatusDoesNotClaimOlderOrManualServersAreStopped(t *testing.T) {
	privateRuntime(t)
	path, config := manualFixture(t)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", config.Listen())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	_, err = Status(t.Context(), path, config)
	if err == nil {
		t.Fatal("unregistered listener was reported as a verified matching proxy")
	}
	message := err.Error()
	if !strings.Contains(message, "no matching registration") || !strings.Contains(message, "configured address is listening") || !strings.Contains(message, "older or manually started proxy") {
		t.Fatalf("misleading status diagnostic: %s", message)
	}
	connection, acceptErr := listener.Accept()
	if acceptErr != nil {
		t.Fatal(acceptErr)
	}
	defer func() { _ = connection.Close() }()
	buffer := make([]byte, 1)
	count, readErr := connection.Read(buffer)
	if count != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatal("status probe sent data or credentials to an unverified listener")
	}
	if strings.Contains(message, "proxy is not running") {
		t.Fatal("status claimed all proxy generations were stopped")
	}
}

func TestMissingRegistrationWithoutAConfiguredListenerIsStillGenerationScoped(t *testing.T) {
	privateRuntime(t)
	path, config := manualFixture(t)
	_, err := Status(t.Context(), path, config)
	if err == nil || !strings.Contains(err.Error(), "no matching registration") || !strings.Contains(err.Error(), "other generations may use different ports") {
		t.Fatalf("ambiguous missing-registration status: %v", err)
	}
}
