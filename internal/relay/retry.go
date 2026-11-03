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
	"time"
)

const (
	// statusOverloaded is Anthropic's "overloaded_error" status, absent from net/http.
	statusOverloaded = 529
	maxRetryAttempts = 5
	retryBaseDelay   = 500 * time.Millisecond
	// retryMaxDelay caps one backoff step and the longest Retry-After the relay waits
	// for. Claude Code retries on its own, so a long relay wait only stalls it.
	retryMaxDelay   = 4 * time.Second
	retryDrainBytes = 64 << 10
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
			_, _ = io.CopyN(io.Discard, resp.Body, retryDrainBytes)
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
		if after, ok := retryAfter(resp.Header); ok {
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

// retryAfter reads the delta-seconds form only; an HTTP date is ignored.
func retryAfter(header http.Header) (time.Duration, bool) {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
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
