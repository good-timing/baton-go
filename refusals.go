package baton

import (
	"log/slog"
	"sync"
	"time"
)

// refusalLogEvery is how often each kind of refusal is logged, as in baton-sdk
// and baton-ts; a collector refusing every batch would otherwise flood the log.
const refusalLogEvery = time.Minute

type refusalLog struct {
	mu   sync.Mutex
	seen map[string]refusalSeen
}

type refusalSeen struct {
	at       time.Time
	unlogged int
}

// take reports whether n refusals of kind are logged now, and how many of
// that kind went unlogged before them.
func (r *refusalLog) take(kind string, n int, now time.Time) (unlogged int, log bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]refusalSeen{}
	}
	seen, ok := r.seen[kind]
	if ok && now.Sub(seen.at) < refusalLogEvery {
		seen.unlogged += n
		r.seen[kind] = seen
		return 0, false
	}
	r.seen[kind] = refusalSeen{at: now}
	return seen.unlogged, true
}

// unlogged returns, and forgets, the refusals not logged yet.
func (r *refusalLog) unlogged() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for kind, seen := range r.seen {
		total += seen.unlogged
		seen.unlogged = 0
		r.seen[kind] = seen
	}
	return total
}

func (s *httpSink) warnRefused(kind string, n int, args ...any) {
	unlogged, log := s.refusals.take(kind, n, time.Now())
	if !log {
		return
	}
	args = append([]any{"events", n}, args...)
	if unlogged > 0 {
		args = append(args, "more_since_last_warning", unlogged)
	}
	slog.Warn("baton: the collector REFUSED events; they have been dropped and are not sent again", args...)
}
