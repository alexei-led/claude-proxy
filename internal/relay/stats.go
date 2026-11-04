package relay

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sort"
	"sync"
	"time"
)

// statsInterval is how often a server logs a traffic summary. Intervals
// without forwarded traffic log nothing.
const statsInterval = 5 * time.Minute

// latencyBounds are histogram bucket upper bounds; a final bucket is open.
var latencyBounds = [...]time.Duration{
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2500 * time.Millisecond, 5 * time.Second, 10 * time.Second,
	30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
}

type histogram [len(latencyBounds) + 1]int64

func (h *histogram) add(d time.Duration) {
	h[sort.Search(len(latencyBounds), func(i int) bool { return d <= latencyBounds[i] })]++
}

// quantile returns the upper bound of the bucket that holds the q-quantile.
// It overstates by at most one bucket; the open bucket reports its lower bound.
func (h *histogram) quantile(q float64) time.Duration {
	var total int64
	for _, count := range h {
		total += count
	}
	if total == 0 {
		return 0
	}
	rank := int64(math.Ceil(q * float64(total)))
	var seen int64
	for i, count := range h {
		seen += count
		if seen >= rank && i < len(latencyBounds) {
			return latencyBounds[i]
		}
	}
	return latencyBounds[len(latencyBounds)-1]
}

// routeKey uses configured labels only, so the number of routes is bounded
// by the configuration, not by client input.
type routeKey struct{ path, model, upstream string }

type routeCounts struct {
	requests int64
	outcomes map[string]int64
	retries  int64
	tokens   tokenCounts
	ttfb     histogram
	duration histogram // forwarded requests only
}

func (c routeCounts) clone() routeCounts {
	c.outcomes = maps.Clone(c.outcomes)
	return c
}

// minus returns the counts accumulated since an earlier copy.
func (c routeCounts) minus(earlier routeCounts) routeCounts {
	delta := c.clone()
	delta.requests -= earlier.requests
	delta.retries -= earlier.retries
	for outcome, count := range earlier.outcomes {
		delta.outcomes[outcome] -= count
	}
	delta.tokens.input -= earlier.tokens.input
	delta.tokens.output -= earlier.tokens.output
	delta.tokens.cacheRead -= earlier.tokens.cacheRead
	delta.tokens.cacheWrite -= earlier.tokens.cacheWrite
	for i := range delta.ttfb {
		delta.ttfb[i] -= earlier.ttfb[i]
		delta.duration[i] -= earlier.duration[i]
	}
	return delta
}

type stats struct {
	mu     sync.Mutex
	since  time.Time
	routes map[routeKey]*routeCounts
}

func newStats(now time.Time) *stats {
	return &stats{since: now, routes: make(map[routeKey]*routeCounts)}
}

func (s *stats) record(kind string, w *responseSummary, result string, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := routeKey{path: kind, model: w.model, upstream: w.upstream}
	counts := s.routes[key]
	if counts == nil {
		counts = &routeCounts{outcomes: make(map[string]int64)}
		s.routes[key] = counts
	}
	counts.requests++
	counts.outcomes[result]++
	counts.retries += int64(w.retries)
	if w.usage != nil && w.usage.seen {
		counts.tokens.input += w.usage.tokens.input
		counts.tokens.output += w.usage.tokens.output
		counts.tokens.cacheRead += w.usage.tokens.cacheRead
		counts.tokens.cacheWrite += w.usage.tokens.cacheWrite
	}
	if w.forwarded {
		counts.duration.add(elapsed)
	}
	if w.ttfb > 0 {
		counts.ttfb.add(w.ttfb)
	}
}

func (s *stats) copyRoutes() map[routeKey]routeCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make(map[routeKey]routeCounts, len(s.routes))
	for key, counts := range s.routes {
		copied[key] = counts.clone()
	}
	return copied
}

func sortedKeys(routes map[routeKey]routeCounts) []routeKey {
	return slices.SortedFunc(maps.Keys(routes), func(a, b routeKey) int {
		return cmp.Or(cmp.Compare(a.path, b.path), cmp.Compare(a.model, b.model), cmp.Compare(a.upstream, b.upstream))
	})
}

// Stats is a cumulative traffic summary for status output. It holds
// configured labels and counts only, never request content.
type Stats struct {
	RegisteredClients int          `json:"registered_clients"`
	ActiveRequests    int          `json:"active_requests"`
	TrackingUncertain bool         `json:"tracking_uncertain"`
	Since             time.Time    `json:"since"`
	Routes            []RouteStats `json:"routes"`
}

// RouteStats counts one path, model and upstream. Latencies are histogram
// bucket upper bounds.
type RouteStats struct {
	Outcomes         map[string]int64 `json:"outcomes"`
	Path             string           `json:"path"`
	Model            string           `json:"model,omitempty"`
	Upstream         string           `json:"upstream,omitempty"`
	TTFBP50          string           `json:"ttfb_p50,omitempty"`
	TTFBP95          string           `json:"ttfb_p95,omitempty"`
	DurationP50      string           `json:"duration_p50,omitempty"`
	DurationP95      string           `json:"duration_p95,omitempty"`
	Requests         int64            `json:"requests"`
	Retries          int64            `json:"retries,omitempty"`
	InputTokens      int64            `json:"input_tokens,omitempty"`
	OutputTokens     int64            `json:"output_tokens,omitempty"`
	CacheReadTokens  int64            `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64            `json:"cache_write_tokens,omitempty"`
}

// SetClients updates the broker's registered process count. Direct clients
// are not registered and cannot be counted while idle.
func (s *Server) SetClients(registered int, uncertain bool) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.registeredClients, s.trackingUncertain = registered, uncertain
}

// Clients returns registered processes, tracking uncertainty and live requests.
func (s *Server) Clients() (registered int, uncertain bool, requests int) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return s.registeredClients, s.trackingUncertain, len(s.slots)
}

// Stats returns cumulative traffic and the current client/request counts.
func (s *Server) Stats() Stats {
	routes := s.stats.copyRoutes()
	result := Stats{Since: s.stats.since, Routes: make([]RouteStats, 0, len(routes))}
	for _, key := range sortedKeys(routes) {
		counts := routes[key]
		result.Routes = append(result.Routes, RouteStats{
			Path: key.path, Model: key.model, Upstream: key.upstream,
			Requests: counts.requests, Outcomes: counts.outcomes, Retries: counts.retries,
			InputTokens: counts.tokens.input, OutputTokens: counts.tokens.output,
			CacheReadTokens: counts.tokens.cacheRead, CacheWriteTokens: counts.tokens.cacheWrite,
			TTFBP50: durationLabel(counts.ttfb.quantile(0.5)), TTFBP95: durationLabel(counts.ttfb.quantile(0.95)),
			DurationP50: durationLabel(counts.duration.quantile(0.5)), DurationP95: durationLabel(counts.duration.quantile(0.95)),
		})
	}
	result.RegisteredClients, result.TrackingUncertain, result.ActiveRequests = s.Clients()
	return result
}

func durationLabel(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// reportStats logs a summary every statsEvery until stop closes, then logs
// the remainder once so a shutdown does not lose the last partial interval.
func (s *Server) reportStats(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(s.statsEvery)
	defer ticker.Stop()
	// Counting starts when the server is created, so the first summary
	// covers everything since then.
	last, lastAt := map[routeKey]routeCounts{}, s.stats.since
	for {
		done := false
		select {
		case <-ticker.C:
		case <-stop:
			done = true
		}
		current, now := s.stats.copyRoutes(), time.Now()
		s.logStats(ctx, last, current, now.Sub(lastAt))
		if done {
			return
		}
		last, lastAt = current, now
	}
}

func (s *Server) logStats(ctx context.Context, last, current map[routeKey]routeCounts, period time.Duration) {
	if s.logger == nil || !s.logger.Enabled(ctx, slog.LevelInfo) {
		return
	}
	registered, uncertain, requests := s.Clients()
	for _, key := range sortedKeys(current) {
		if key.path == kindModels {
			continue // catalog reads are local and say nothing about providers
		}
		delta := current[key].minus(last[key])
		if delta.requests == 0 {
			continue
		}
		attrs := []slog.Attr{
			slog.Int("registered_clients", registered), slog.Bool("tracking_uncertain", uncertain), slog.Int("active_requests", requests),
			slog.String("profile", s.config.profile), slog.String("path", key.path),
			slog.String("model", key.model), slog.String("upstream", key.upstream),
			slog.Duration("period", period.Round(time.Second)), slog.Int64("requests", delta.requests),
			slog.Int64("failed", delta.requests-delta.outcomes[outcomeOK]-delta.outcomes[outcomeClientClosed]),
			slog.Int64("client_closed", delta.outcomes[outcomeClientClosed]), slog.Int64("retries", delta.retries),
		}
		if delta.tokens != (tokenCounts{}) {
			attrs = append(attrs, slog.Int64("input_tokens", delta.tokens.input), slog.Int64("output_tokens", delta.tokens.output),
				slog.Int64("cache_read_tokens", delta.tokens.cacheRead), slog.Int64("cache_write_tokens", delta.tokens.cacheWrite))
		}
		if p50 := delta.ttfb.quantile(0.5); p50 > 0 {
			attrs = append(attrs, slog.Duration("ttfb_p50", p50), slog.Duration("ttfb_p95", delta.ttfb.quantile(0.95)))
		}
		if p95 := delta.duration.quantile(0.95); p95 > 0 {
			attrs = append(attrs, slog.Duration("duration_p95", p95))
		}
		s.logger.LogAttrs(ctx, slog.LevelInfo, "stats", attrs...)
	}
}
