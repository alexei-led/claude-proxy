package relay

import (
	"encoding/json"
	"net/http"
	"time"
)

// responseSummary records what the access log and stats need about one
// request while passing the response through unchanged.
type responseSummary struct {
	http.ResponseWriter
	start        time.Time
	status       int
	bytes        int
	requestBytes int
	category     string // set only for responses the relay generated itself
	model        string
	upstream     string
	retries      int
	retryReasons []string
	forwarded    bool          // the request was handed to the upstream proxy
	ttfb         time.Duration // until upstream response headers reached the client
	requestID    string
	writeFailed  bool      // writing to the client failed: the client left
	usage        *usageTap // only for forwarded Messages requests
}

func (w *responseSummary) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseSummary) WriteHeader(status int) {
	// ReverseProxy forwards 1xx headers before ModifyResponse is called.
	stripCredentials(w.Header())
	if status >= 200 && w.status == 0 {
		w.status = status
		if w.forwarded && w.category == "" {
			w.ttfb = time.Since(w.start)
			w.requestID = upstreamRequestID(w.Header())
			if w.usage != nil {
				w.usage.begin(w.Header())
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseSummary) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	if err != nil {
		w.writeFailed = true
	}
	if w.usage != nil && w.category == "" {
		w.usage.write(body[:n])
	}
	return n, err
}

func (w *responseSummary) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.writeFailed = true
	}
	return err
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
	case http.StatusServiceUnavailable:
		return apiError, "Relay is shutting down."
	case statusClientClosed:
		return apiError, "Client closed the request."
	case http.StatusBadGateway:
		return apiError, "Upstream connection failed."
	case http.StatusGatewayTimeout:
		return "timeout_error", "Upstream response timed out."
	default:
		return invalidRequestError, "Invalid request."
	}
}
