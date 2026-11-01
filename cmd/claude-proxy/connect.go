package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

const (
	settingsFlag      = "--settings"
	baseURLEnv        = "ANTHROPIC_BASE_URL"
	customHeadersEnv  = "ANTHROPIC_CUSTOM_HEADERS"
	routerProviderEnv = "CLAUDE_ROUTER_PROVIDER"
	proxyProvider     = "model-proxy"
	localKeyHeader    = "X-Claude-Proxy-Key"
)

func connect(ctx context.Context, args []string) error {
	environment, command, err := prepareConnect(ctx, args, os.Environ())
	if err != nil {
		return err
	}
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return errors.New("client executable is unavailable")
	}
	// #nosec G204 -- The operator explicitly selects this command; no shell is invoked.
	if err := syscall.Exec(executable, command, environment); err != nil {
		return errors.New("client execution failed")
	}
	return nil
}

func prepareConnect(ctx context.Context, args, parent []string) ([]string, []string, error) {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration file")
	if err := flags.Parse(args); err != nil || *path == "" || flags.NArg() == 0 {
		return nil, nil, errors.New("expected connect --config PATH -- COMMAND [ARGS]")
	}
	config, err := relay.LoadClientConfig(*path)
	if err != nil {
		return nil, nil, err
	}
	environment, err := clientEnvironment(parent, config)
	if err != nil {
		return nil, nil, err
	}
	if err := checkSettingsArguments(flags.Args()); err != nil {
		return nil, nil, err
	}
	if err := checkCatalog(ctx, config); err != nil {
		return nil, nil, err
	}
	return environment, flags.Args(), nil
}

func clientEnvironment(parent []string, config *relay.ClientConfig) ([]string, error) {
	values := make(map[string]string, len(parent))
	for _, item := range parent {
		if name, value, ok := strings.Cut(item, "="); ok {
			values[name] = value
		}
	}
	if values["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		return nil, errors.New("remove the explicit OAuth override before connecting")
	}
	for _, name := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if value := strings.ToLower(values[name]); value != "" && value != "0" && value != "false" {
			return nil, errors.New("remove conflicting backend settings before connecting")
		}
	}
	if value := values[baseURLEnv]; value != "" && value != config.BaseURL {
		return nil, errors.New("remove the conflicting base URL before connecting")
	}
	headers, err := clientHeaders(values[customHeadersEnv], config.LocalKey)
	if err != nil {
		return nil, err
	}
	delete(values, "ANTHROPIC_API_KEY")
	delete(values, "ANTHROPIC_AUTH_TOKEN")
	values[baseURLEnv] = config.BaseURL
	values[customHeadersEnv] = headers
	values[routerProviderEnv] = proxyProvider
	environment := make([]string, 0, len(values))
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func clientHeaders(existing, token string) (string, error) {
	headers := make([]string, 0)
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return "", errors.New("invalid custom headers")
		}
		for _, ch := range name {
			if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", ch) {
				return "", errors.New("invalid custom headers")
			}
		}
		for _, ch := range value {
			if ch < 0x20 && ch != '\t' || ch > 0x7e {
				return "", errors.New("invalid custom headers")
			}
		}
		switch strings.ToLower(name) {
		case "x-claude-proxy-key", "authorization", "x-api-key", "proxy-authorization", "cookie", "host", "connection", "content-length", "transfer-encoding":
			return "", errors.New("remove reserved custom headers before connecting")
		}
		headers = append(headers, line)
	}
	return strings.Join(append(headers, localKeyHeader+": "+token), "\n"), nil
}

// Settings checks catch operator mistakes, not same-user filesystem races.
// Claude Code also reads its own user/project/managed settings after exec.
func checkSettingsArguments(args []string) error {
	for index, arg := range args {
		var value string
		switch {
		case arg == settingsFlag:
			if index+1 >= len(args) {
				return errors.New("invalid client settings argument")
			}
			value = args[index+1]
		case strings.HasPrefix(arg, "--settings="):
			value = strings.TrimPrefix(arg, "--settings=")
		default:
			continue
		}
		body := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			// #nosec G304 -- The operator selects this bounded, read-only settings file.
			file, err := os.OpenFile(value, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return errors.New("client settings are unavailable")
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				_ = file.Close()
				return errors.New("client settings must be a regular file")
			}
			body, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
			_ = file.Close()
			if err != nil || len(body) > 1<<20 {
				return errors.New("client settings are unavailable")
			}
		}
		var settings struct {
			Env          map[string]string `json:"env"`
			APIKeyHelper json.RawMessage   `json:"apiKeyHelper"`
		}
		if err := json.Unmarshal(body, &settings); err != nil {
			return errors.New("invalid client settings")
		}
		if len(settings.APIKeyHelper) != 0 {
			return errors.New("client settings cannot override proxy authentication")
		}
		for name := range settings.Env {
			switch name {
			case baseURLEnv, customHeadersEnv, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CONFIG_DIR", "HOME", routerProviderEnv:
				return errors.New("client settings cannot override proxy connection or profile")
			}
		}
	}
	return nil
}

func checkCatalog(ctx context.Context, config *relay.ClientConfig) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.BaseURL+"/v1/models?limit=1000", nil)
	if err != nil {
		return errors.New("cannot prepare local catalog request")
	}
	request.Header.Set(localKeyHeader, config.LocalKey)
	transport := &http.Transport{
		Proxy:              nil,
		DialContext:        (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		DisableCompression: true,
		DisableKeepAlives:  true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("local proxy is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("local proxy catalog did not accept the connection")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return errors.New("invalid local proxy catalog")
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil || catalog.HasMore {
		return errors.New("invalid local proxy catalog")
	}
	models := make(map[string]bool, len(catalog.Data))
	for _, model := range catalog.Data {
		models[model.ID] = true
	}
	for _, id := range config.Models {
		if !models[id] {
			return errors.New("local proxy catalog does not match the selected configuration")
		}
	}
	return nil
}
