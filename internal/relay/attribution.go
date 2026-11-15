package relay

import (
	"encoding/json"
	"net/http"
	"strings"
)

// modelLabel drops unsafe or oversized reports rather than truncating them
// into a different identity. It permits native IDs, org prefixes and context hints.
func modelLabel(value string) string {
	const (
		maxModelBytes = 128
		modelChars    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.:/[]"
	)
	if len(value) > maxModelBytes || strings.Trim(value, modelChars) != "" {
		return ""
	}
	return value
}

// headerReportedModel is an upstream claim, not independent backend attestation.
// Only this known header is inspected; duplicate or combined values are omitted.
func headerReportedModel(header http.Header) string {
	values := header.Values("X-Valar-Served-Model")
	if len(values) != 1 {
		return ""
	}
	return modelLabel(values[0])
}

// bodyReportedModel ignores invalid model types without losing token usage.
func bodyReportedModel(raw json.RawMessage) string {
	var model string
	if json.Unmarshal(raw, &model) != nil {
		return ""
	}
	return modelLabel(model)
}
