package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxBodyBytes      = 32 << 20
	maxInflight       = 8
	maxHeaderBytes    = 64 << 10
	readHeaderTimeout = 5 * time.Second
	readBodyTimeout   = 30 * time.Second
)

// Server owns one profile's immutable routes and bounded transport.
type Server struct {
	config    *Config
	logger    *slog.Logger
	transport *http.Transport
	proxies   map[*url.URL]*httputil.ReverseProxy
	slots     chan struct{}
	catalog   []byte
}

// NewServer creates a relay without binding or making outbound calls.
func NewServer(config *Config, logger *slog.Logger) *Server {
	server := &Server{
		config: config, logger: logger, transport: newTransport(),
		proxies: make(map[*url.URL]*httputil.ReverseProxy), slots: make(chan struct{}, maxInflight),
		catalog: catalog(config),
	}
	for _, entry := range config.models {
		route := entry.upstream
		for _, target := range []*url.URL{route.messages, route.count} {
			if target != nil {
				server.proxies[target] = server.proxy(route, target)
			}
		}
	}
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	summary := &responseSummary{ResponseWriter: w}
	start := time.Now()
	kind := pathKind(r)
	defer func() {
		s.logger.Info("request", "profile", s.config.profile, "method", methodKind(r.Method),
			"path", kind, "status", summary.status, "elapsed", time.Since(start),
			"request_bytes", summary.requestBytes, "response_bytes", summary.bytes, "category", summary.category,
			"model", summary.model, "upstream", summary.upstream)
	}()
	if !localAccess(r.Header, s.config.token) {
		localError(summary, http.StatusUnauthorized)
		return
	}
	if !localBoundary(r, s.config.listen) {
		localError(summary, http.StatusForbidden)
		return
	}
	if !validCredentials(r) {
		localError(summary, http.StatusBadRequest)
		return
	}
	if kind == kindUnknown {
		localError(summary, http.StatusNotFound)
		return
	}
	method := http.MethodPost
	if kind == kindModels {
		method = http.MethodGet
	}
	if r.Method != method {
		summary.Header().Set("Allow", method)
		localError(summary, http.StatusMethodNotAllowed)
		return
	}
	if kind == kindModels {
		summary.Header().Set("Content-Type", "application/json")
		summary.Header().Set("Cache-Control", "no-store")
		_, _ = summary.Write(s.catalog)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		localError(summary, http.StatusTooManyRequests)
		return
	}
	s.forward(summary, r, kind)
}

func (s *Server) forward(w *responseSummary, r *http.Request, kind string) {
	if !jsonRequest(r) {
		localError(w, http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > maxBodyBytes {
		localError(w, http.StatusRequestEntityTooLarge)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(readBodyTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	_ = controller.SetReadDeadline(time.Time{})
	w.requestBytes = len(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			localError(w, http.StatusRequestEntityTooLarge)
		} else {
			localError(w, http.StatusBadRequest)
		}
		return
	}
	id, err := requestModel(body)
	if err != nil {
		localError(w, http.StatusBadRequest)
		return
	}
	entry, ok := s.config.models[id]
	if !ok {
		localError(w, http.StatusNotFound)
		return
	}
	w.model = boundedLabel(id)
	w.upstream = entry.label
	target := entry.upstream.messages
	if kind == "count_tokens" {
		target = entry.upstream.count
	}
	if target == nil {
		localError(w, http.StatusNotFound)
		return
	}
	if entry.upstream.mode == authOAuth {
		if _, valid := oauthAuthorization(r.Header); !valid {
			localError(w, http.StatusUnauthorized)
			return
		}
	}
	// A non-replayable body prevents Transport from retrying a generation even
	// when the client supplies an idempotency header.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = nil
	s.proxies[target].ServeHTTP(w, r)
}

func jsonRequest(r *http.Request) bool {
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) > 1 {
		return false
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	encoding := r.Header.Get("Content-Encoding")
	return err == nil && media == "application/json" && (encoding == "" || strings.EqualFold(encoding, "identity"))
}

func pathKind(r *http.Request) string {
	if r.URL.EscapedPath() != r.URL.Path {
		return kindUnknown
	}
	switch r.URL.Path {
	case modelsPath:
		return kindModels
	case messagesPath:
		return "messages"
	case countTokensPath:
		return "count_tokens"
	default:
		return kindUnknown
	}
}

func methodKind(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodConnect, http.MethodPatch:
		return method
	default:
		return "other"
	}
}

func localBoundary(r *http.Request, listen string) bool {
	if r.Host != listen {
		return false
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Origin") || strings.HasPrefix(strings.ToLower(key), "sec-fetch-") {
			return false
		}
	}
	return true
}

func validCredentials(r *http.Request) bool {
	for _, key := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization"} {
		if len(r.Header.Values(key)) > 1 {
			return false
		}
	}
	if len(r.Header.Values("Upgrade")) > 0 || len(r.Header.Values("Cookie")) > 0 || len(r.Header.Values("Proxy-Authorization")) > 0 {
		return false
	}
	// Inspect only parameter names. Parsing the whole query as form data
	// would reject otherwise valid raw semicolon values and alter fidelity.
	// Treat both common separators as boundaries when looking for credentials.
	for pair := range strings.FieldsFuncSeq(r.URL.RawQuery, func(ch rune) bool { return ch == '&' || ch == ';' }) {
		name, _, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(name)
		if err != nil {
			return false
		}
		switch strings.ToLower(key) {
		case "authorization", "auth", "api_key", "api-key", "apikey", "x-api-key", "access_token", "token", "key", "x-claude-proxy-key":
			return false
		}
	}
	return true
}

func requestModel(body []byte) (string, error) {
	if !utf8.Valid(body) {
		return "", errors.New("request must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("expected request object")
	}
	id := ""
	seen := false
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return "", errors.New("invalid request key")
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", errors.New("invalid request key")
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return "", errors.New("invalid request JSON")
		}
		if !strings.EqualFold(key, "model") {
			continue
		}
		if key != "model" || seen {
			return "", errors.New("ambiguous model")
		}
		seen = true
		if err = json.Unmarshal(value, &id); err != nil || id == "" {
			return "", errors.New("model must be a nonempty string")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return "", errors.New("invalid request object")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) || !seen {
		return "", errors.New("expected exactly one model and object")
	}
	return id, nil
}
