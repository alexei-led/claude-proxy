// Package relay implements a local native Anthropic transport.
package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxConfigBytes = 1 << 20
	maxSecretBytes = 16 << 10
)

type authConfig struct {
	Mode    string `json:"mode"`
	KeyFile string `json:"key_file,omitempty"`
}
type upstreamConfig struct {
	MessagesURL    string     `json:"messages_url"`
	CountTokensURL string     `json:"count_tokens_url,omitempty"`
	Auth           authConfig `json:"auth"`
}
type modelConfig struct {
	Upstream    string `json:"upstream"`
	DisplayName string `json:"display_name,omitempty"`
}
type fileConfig struct {
	Version         int                       `json:"version"`
	Profile         string                    `json:"profile"`
	Listen          string                    `json:"listen"`
	AccessTokenFile string                    `json:"access_token_file"`
	Upstreams       map[string]upstreamConfig `json:"upstreams"`
	Models          map[string]modelConfig    `json:"models"`
}
type upstream struct {
	messages *url.URL
	count    *url.URL
	mode     string
	key      string
}
type model struct {
	upstream    *upstream
	label       string
	displayName string
}

// Config is a validated, immutable snapshot of one profile.
type Config struct {
	profile string
	listen  string
	token   string
	models  map[string]model
}

// Profile returns the selected profile label.
func (c *Config) Profile() string { return c.profile }

// Listen returns the validated loopback listener address.
func (c *Config) Listen() string { return c.listen }

// LoadConfig validates and snapshots one profile without network access.
// Errors deliberately omit paths, supplied values and secret contents.
func LoadConfig(path string) (*Config, error) {
	body, err := readPrivate(path, false, maxConfigBytes)
	if err != nil {
		return nil, errors.New("configuration file is unavailable or unsafe")
	}
	if err = validateConfigJSON(body); err != nil {
		return nil, err
	}
	var raw fileConfig
	if err = json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid configuration schema")
	}
	if raw.Version != 1 || (raw.Profile != personalProfile && raw.Profile != workProfile) {
		return nil, errors.New("unsupported configuration version or profile")
	}
	host, port, err := net.SplitHostPort(raw.Listen)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host != "127.0.0.1" || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return nil, errors.New("listener must be 127.0.0.1 with an explicit nonzero port")
	}
	if raw.AccessTokenFile == "" || len(raw.Models) == 0 || len(raw.Upstreams) == 0 {
		return nil, errors.New("configuration requires an access token, upstreams and models")
	}
	token, err := loadSecret(path, raw.AccessTokenFile)
	if err != nil {
		return nil, err
	}
	compiled := make(map[string]*upstream, len(raw.Upstreams))
	for label, entry := range raw.Upstreams {
		if !safeLabel(label) {
			return nil, errors.New("invalid upstream label")
		}
		messages, urlErr := endpoint(entry.MessagesURL)
		if urlErr != nil {
			return nil, urlErr
		}
		var count *url.URL
		if entry.CountTokensURL != "" {
			count, err = endpoint(entry.CountTokensURL)
			if err != nil {
				return nil, err
			}
		}
		route := &upstream{messages: messages, count: count, mode: entry.Auth.Mode}
		switch route.mode {
		case authOAuth, authNone:
			if entry.Auth.KeyFile != "" {
				return nil, errors.New("auth mode forbids a key file")
			}
		case authBearer, authAPIKey:
			if entry.Auth.KeyFile == "" {
				return nil, errors.New("auth mode requires a key file")
			}
			route.key, err = loadSecret(path, entry.Auth.KeyFile)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("unsupported authentication mode")
		}
		compiled[label] = route
	}
	config := &Config{profile: raw.Profile, listen: raw.Listen, token: token, models: make(map[string]model, len(raw.Models))}
	for id, entry := range raw.Models {
		route, ok := compiled[entry.Upstream]
		if id == "" || !ok {
			return nil, errors.New("empty model ID or missing upstream reference")
		}
		config.models[id] = model{upstream: route, label: entry.Upstream, displayName: entry.DisplayName}
	}
	return config, nil
}

func safeLabel(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

func endpoint(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") || parsed.Path == "" {
		return nil, errors.New("invalid upstream endpoint")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !net.ParseIP(parsed.Hostname()).IsLoopback()) {
		return nil, errors.New("upstream requires HTTPS or literal loopback HTTP")
	}
	if port := parsed.Port(); port != "" {
		number, portErr := strconv.Atoi(port)
		if portErr != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid upstream port")
		}
	}
	return parsed, nil
}

func loadSecret(configPath, secretPath string) (string, error) {
	if !filepath.IsAbs(secretPath) {
		secretPath = filepath.Join(filepath.Dir(configPath), secretPath)
	}
	body, err := readPrivate(secretPath, true, maxSecretBytes)
	if err != nil {
		return "", errors.New("secret file is unavailable or unsafe")
	}
	value := string(body)
	if strings.HasSuffix(value, "\r\n") {
		value = strings.TrimSuffix(value, "\r\n")
	} else {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return "", errors.New("secret must not be empty")
	}
	for _, ch := range value {
		if ch < 0x20 || ch == 0x7f || ch > 0x7e {
			return "", errors.New("secret must contain printable header bytes")
		}
	}
	return value, nil
}

// validateConfigJSON enforces exact field names; encoding/json alone accepts
// case-insensitive matches. The token walk also catches duplicate object keys.
func validateConfigJSON(body []byte) error {
	if !utf8.Valid(body) {
		return errors.New("configuration must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := jsonValue(decoder, 0)
	if err != nil {
		return errors.New("invalid or duplicate configuration JSON")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("configuration must contain one object")
	}
	root, ok := value.(map[string]any)
	if !ok || !onlyFields(root, "version", "profile", "listen", "access_token_file", "upstreams", "models") {
		return errors.New("unknown configuration fields")
	}
	upstreams, ok := root["upstreams"].(map[string]any)
	if !ok {
		return errors.New("upstreams must be an object")
	}
	for _, v := range upstreams {
		entry, isObject := v.(map[string]any)
		if !isObject || !onlyFields(entry, "messages_url", "count_tokens_url", "auth") {
			return errors.New("unknown upstream fields")
		}
		auth, isAuth := entry["auth"].(map[string]any)
		if !isAuth || !onlyFields(auth, "mode", "key_file") {
			return errors.New("unknown authentication fields")
		}
	}
	models, ok := root["models"].(map[string]any)
	if !ok {
		return errors.New("models must be an object")
	}
	for _, v := range models {
		entry, isObject := v.(map[string]any)
		if !isObject || !onlyFields(entry, "upstream", "display_name") {
			return errors.New("unknown model fields")
		}
	}
	return nil
}

func onlyFields(object map[string]any, allowed ...string) bool {
	for key, value := range object {
		found := false
		for _, field := range allowed {
			if key == field {
				found = true
				break
			}
		}
		if !found || value == nil {
			return false
		}
	}
	return true
}

func jsonValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 100 {
		return nil, errors.New("JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, isString := keyToken.(string)
			if !isString {
				return nil, errors.New("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, errors.New("duplicate object key")
			}
			object[key], err = jsonValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		for decoder.More() {
			if _, err = jsonValue(decoder, depth+1); err != nil {
				return nil, err
			}
		}
		if _, err = decoder.Token(); err != nil {
			return nil, err
		}
		return []any{}, nil
	default:
		return nil, errors.New("invalid JSON delimiter")
	}
}
