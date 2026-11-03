package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// statusOverloaded is Anthropic's "overloaded_error" status, absent from net/http.
	statusOverloaded = 529
	maxRetryAttempts = 5
	retryBaseDelay   = 500 * time.Millisecond
	// retryMaxDelay caps one backoff step and the longest Retry-After the relay waits
	// for. Claude Code retries on its own, so a long relay wait only stalls it.
	retryMaxDelay = 4 * time.Second
	// retryAfterCeiling bounds a parsed Retry-After before it becomes a Duration,
	// which would otherwise overflow and wrap negative. Anything this long is passed through.
	retryAfterCeiling = time.Hour
)

type replayKey struct{}

// replay hands the buffered request body to the retrying transport and carries
// the retry count back to the request log. The transport runs on the handler
// goroutine, so no locking is needed.
type replay struct {
	body    []byte
	retries int
}

// retrier re-sends a request the upstream provably did not start generating:
// a 503 or 529 answer, or a failed dial. Anything later, such as a timeout or
// reset after the request left, may already be billed and is never retried.
type retrier struct {
	base     http.RoundTripper
	attempts int
	delay    time.Duration
}

func (r *retrier) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	ctx := req.Context()
	state, ok := ctx.Value(replayKey{}).(*replay)
	if !ok {
		return r.base.RoundTrip(req)
	}
	for attempt := 1; ; attempt++ {
		out := req.Clone(ctx)
		out.Body = io.NopCloser(bytes.NewReader(state.body))
		resp, err := r.base.RoundTrip(out)
		wait, again := r.next(ctx, attempt, resp, err)
		if !again {
			return resp, err
		}
		if resp != nil {
			// Close without reading: an upstream that sends 503 headers and then stalls
			// must not hold the request. The cost is one connection that is not reused.
			_ = resp.Body.Close()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		state.retries++
	}
}

func (r *retrier) next(ctx context.Context, attempt int, resp *http.Response, err error) (time.Duration, bool) {
	if attempt >= r.attempts || ctx.Err() != nil {
		return 0, false
	}
	wait := r.backoff(attempt)
	if resp != nil {
		if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != statusOverloaded {
			return 0, false
		}
		if after, ok := retryAfter(resp.Header, time.Now()); ok {
			if after > retryMaxDelay {
				return 0, false
			}
			wait = max(wait, after)
		}
		return wait, true
	}
	var op *net.OpError
	return wait, errors.As(err, &op) && op.Op == "dial"
}

// backoff doubles per attempt up to retryMaxDelay, with jitter in [d/2, d] so
// concurrent requests do not hit a recovering provider in lockstep.
func (r *retrier) backoff(attempt int) time.Duration {
	d := min(r.delay<<(attempt-1), retryMaxDelay)
	return d/2 + rand.N(d/2+1) // #nosec G404 -- Jitter needs spread, not unpredictability.
}

// retryAfter reads both RFC 9110 forms, delta-seconds and an HTTP date. An
// unparseable value is ignored.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := header.Get("Retry-After")
	seconds, err := strconv.ParseInt(value, 10, 64)
	switch {
	case strings.HasPrefix(value, "-"):
		return 0, false
	case errors.Is(err, strconv.ErrRange):
		return retryAfterCeiling, true
	case err == nil:
		return time.Duration(min(seconds, int64(retryAfterCeiling/time.Second))) * time.Second, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return min(max(at.Sub(now), 0), retryAfterCeiling), true
}

// retryAttempts validates the optional per-upstream retry block. One attempt means no retry.
func retryAttempts(raw *retryConfig) (int, error) {
	if raw == nil {
		return 1, nil
	}
	if raw.MaxAttempts < 1 || raw.MaxAttempts > maxRetryAttempts {
		return 0, errors.New("retry max_attempts must be between 1 and 5")
	}
	return raw.MaxAttempts, nil
}
