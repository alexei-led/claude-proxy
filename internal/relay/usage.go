package relay

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

const (
	// maxTapJSONBytes bounds the copy of a non-streaming response kept for its
	// usage; a larger response is forwarded unchanged and counts no tokens.
	maxTapJSONBytes = 1 << 20
	// maxTapLineBytes bounds one SSE line. Usage lives in short message_start,
	// message_delta and error lines; longer lines are skipped.
	maxTapLineBytes = 64 << 10
	maxTokenLabel   = 64
	// eventError is the SSE event an upstream sends when a stream fails.
	eventError = "error"
)

const (
	tapOff = iota
	tapJSON
	tapSSE
)

type tokenCounts struct {
	input, output, cacheRead, cacheWrite int64
}

// usageTap reads token usage, stop reason and error type from a forwarded
// Messages response as it passes. It never changes or delays the bytes.
type usageTap struct {
	mode       int
	buffer     []byte
	overflow   bool
	event      string
	seen       bool
	tokens     tokenCounts
	stopReason string
	errorType  string
}

type usageFields struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}

type errorField struct {
	Type string `json:"type"`
}

func (tap *usageTap) begin(header http.Header) {
	media, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	switch {
	case err != nil:
	case media == "text/event-stream":
		tap.mode = tapSSE
	case media == "application/json":
		tap.mode = tapJSON
	}
}

func (tap *usageTap) write(body []byte) {
	switch tap.mode {
	case tapJSON:
		if tap.overflow || len(tap.buffer)+len(body) > maxTapJSONBytes {
			tap.overflow, tap.buffer = true, nil
			return
		}
		tap.buffer = append(tap.buffer, body...)
	case tapSSE:
		for len(body) > 0 {
			end := bytes.IndexByte(body, '\n')
			if end < 0 {
				tap.keep(body)
				return
			}
			tap.keep(body[:end])
			if !tap.overflow {
				tap.line(bytes.TrimSuffix(tap.buffer, []byte("\r")))
			}
			tap.buffer, tap.overflow = tap.buffer[:0], false
			body = body[end+1:]
		}
	}
}

func (tap *usageTap) keep(part []byte) {
	if tap.overflow || len(tap.buffer)+len(part) > maxTapLineBytes {
		tap.overflow = true
		return
	}
	tap.buffer = append(tap.buffer, part...)
}

func (tap *usageTap) line(line []byte) {
	field, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(field) {
	case "":
		if len(line) == 0 {
			tap.event = "" // a blank line ends the event
		}
	case "event":
		tap.event = string(value)
	case "data":
		var data struct {
			Message *struct {
				Usage *usageFields `json:"usage"`
			} `json:"message"`
			Delta *struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *usageFields `json:"usage"`
			Error *errorField  `json:"error"`
		}
		switch tap.event {
		case "message_start", "message_delta", eventError:
		default:
			return
		}
		if json.Unmarshal(value, &data) != nil {
			return
		}
		if data.Message != nil {
			tap.apply(data.Message.Usage)
		}
		tap.apply(data.Usage)
		if data.Delta != nil {
			tap.setStop(data.Delta.StopReason)
		}
		if data.Error != nil {
			tap.errorType = tokenLabel(data.Error.Type)
		}
	}
}

// finish parses a buffered JSON body. Streams were parsed line by line.
func (tap *usageTap) finish() {
	if tap.mode != tapJSON || tap.overflow || len(tap.buffer) == 0 {
		return
	}
	var body struct {
		StopReason string       `json:"stop_reason"`
		Usage      *usageFields `json:"usage"`
		Error      *errorField  `json:"error"`
	}
	if json.Unmarshal(tap.buffer, &body) == nil {
		tap.apply(body.Usage)
		tap.setStop(body.StopReason)
		if body.Error != nil {
			tap.errorType = tokenLabel(body.Error.Type)
		}
	}
	tap.buffer = nil
}

// apply overwrites known counts: message_delta carries cumulative totals.
func (tap *usageTap) apply(fields *usageFields) {
	if fields == nil {
		return
	}
	for _, pair := range []struct {
		from *int64
		to   *int64
	}{
		{fields.Input, &tap.tokens.input}, {fields.Output, &tap.tokens.output},
		{fields.CacheRead, &tap.tokens.cacheRead}, {fields.CacheWrite, &tap.tokens.cacheWrite},
	} {
		if pair.from != nil && *pair.from >= 0 {
			*pair.to = *pair.from
			tap.seen = true
		}
	}
}

func (tap *usageTap) setStop(reason string) {
	if label := tokenLabel(reason); label != "" {
		tap.stopReason = label
	}
}

// tokenLabel admits only short lowercase protocol words, such as
// overloaded_error or end_turn; anything else from a provider is dropped.
func tokenLabel(value string) string {
	const tokenChars = "abcdefghijklmnopqrstuvwxyz0123456789_"
	if value == "" || len(value) > maxTokenLabel || strings.Trim(value, tokenChars) != "" {
		return ""
	}
	return value
}

// upstreamRequestID returns the provider's request ID, the value its support
// asks for. It is an opaque label, never a credential.
func upstreamRequestID(header http.Header) string {
	const (
		maxRequestID   = 128
		requestIDChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.:"
	)
	for _, name := range []string{"Request-Id", "X-Request-Id"} {
		value := header.Get(name)
		if value != "" && len(value) <= maxRequestID && strings.Trim(value, requestIDChars) == "" {
			return value
		}
	}
	return ""
}
