package relay

import (
	"encoding/json"
	"net/http"
)

type responseSummary struct {
	http.ResponseWriter
	status       int
	bytes        int
	requestBytes int
	category     string
	model        string
	upstream     string
}

func (w *responseSummary) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseSummary) WriteHeader(status int) {
	// ReverseProxy forwards 1xx headers before ModifyResponse is called.
	stripCredentials(w.Header())
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseSummary) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *responseSummary) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func localError(w http.ResponseWriter, status int) {
	kind, message := errorDetails(status)
	if summary, ok := w.(*responseSummary); ok {
		summary.category = kind
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}{Type: "error", Error: struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: kind, Message: message}})
}

func errorDetails(status int) (string, string) {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error", "Authentication required."
	case http.StatusForbidden:
		return "permission_error", "Local request rejected."
	case http.StatusNotFound:
		return "not_found_error", "Route not found."
	case http.StatusMethodNotAllowed:
		return invalidRequestError, "Method not allowed."
	case http.StatusRequestEntityTooLarge:
		return "request_too_large", "Request body too large."
	case http.StatusUnsupportedMediaType:
		return invalidRequestError, "Unsupported request content type or encoding."
	case http.StatusTooManyRequests:
		return "rate_limit_error", "Relay is at capacity."
	case http.StatusBadGateway:
		return "api_error", "Upstream connection failed."
	case http.StatusGatewayTimeout:
		return "timeout_error", "Upstream response timed out."
	default:
		return invalidRequestError, "Invalid request."
	}
}
