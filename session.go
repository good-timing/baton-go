package baton

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func uuid7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = 0x70 | b[6]&0x0f
	b[8] = 0x80 | b[8]&0x3f
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	hex.Encode(out[9:13], b[4:6])
	hex.Encode(out[14:18], b[6:8])
	hex.Encode(out[19:23], b[8:10])
	hex.Encode(out[24:], b[10:])
	out[8], out[13], out[18], out[23] = '-', '-', '-', '-'
	return string(out[:])
}

var (
	transportHTTP   = "http"
	transportNoHTTP = "no-http-request"
)

type callSession struct {
	id     string
	origin string // "observed" or "minted"
	// transport is transport_observed: nil when the producer cannot tell.
	transport *string
}

// perRequest is SPEC §3.4 rung 5's per-call session: minted under HTTP.
func (c callSession) perRequest() bool {
	return c.origin == "minted" && c.transport == &transportHTTP
}

func mintSessionID() string { return "sdk-" + uuid7() }

// sessions resolves SPEC §3.4's ladder as far as go-sdk shows it:
//   - the Mcp-Session-Id header (rung 4);
//   - else, under HTTP, a new id per request (rung 5);
//   - else, with no HTTP request (stdio, in-memory), the process-wide id, which
//     the surface_snapshot shares, as in baton-sdk;
//   - else (SSE, which passes no headers) an id per go-sdk connection.
//
// The header is read directly because ServerSession.ID cannot tell an id the
// client sent from one go-sdk made up. Weak keys let a closed connection be
// collected.
type sessions struct {
	fallback string
	// ioConnType is go-sdk's unexported stdio and in-memory connection type,
	// taken from the probe's connection; nil if the probe failed.
	ioConnType reflect.Type
	minted     *lru[weak.Pointer[mcp.ServerSession], string]
}

const sessionCap = 4096

func newSessions() *sessions {
	return &sessions{fallback: mintSessionID(), minted: newLRU[weak.Pointer[mcp.ServerSession], string](sessionCap)}
}

func (s *sessions) resolve(conn *mcp.ServerSession, header http.Header) callSession {
	if header != nil {
		if id := strings.TrimSpace(header.Get("Mcp-Session-Id")); id != "" {
			return callSession{id: id, origin: "observed", transport: &transportHTTP}
		}
		return callSession{id: mintSessionID(), origin: "minted", transport: &transportHTTP}
	}
	if conn == nil {
		return s.process()
	}
	if s.ioConnType != nil && connType(conn) == s.ioConnType {
		return callSession{id: s.fallback, origin: "minted", transport: &transportNoHTTP}
	}
	return callSession{id: s.minted.getOrPut(weak.Make(conn), mintSessionID), origin: "minted"}
}

// process is the process-wide session, for events that belong to no caller.
func (s *sessions) process() callSession {
	return callSession{id: s.fallback, origin: "minted"}
}

// connType is the dynamic type of the session's connection. go-sdk does not
// export it, so it is read by reflection; a renamed field gives nil, and the
// transport is then reported as unknown.
func connType(conn *mcp.ServerSession) reflect.Type {
	field := reflect.ValueOf(conn).Elem().FieldByName("mcpConn")
	if !field.IsValid() || field.IsNil() {
		return nil
	}
	return field.Elem().Type()
}

// sequencer numbers each session's events from 1 (SPEC §11.2 rule 3). An
// observed id can span processes, so its number is also at least the time in
// microseconds; a per-request session is numbered by the clock alone (SPEC
// §3.4 rung 5), so it keeps no entry.
type sequencer struct {
	counts *lru[string, int64]
	clock  microClock
}

// microClock numbers by the time in microseconds, at least one more each call.
type microClock struct {
	mu   sync.Mutex
	last int64
}

func (c *microClock) next() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = max(c.last+1, time.Now().UnixMicro())
	return c.last
}

func newSequencer() *sequencer {
	return &sequencer{counts: newLRU[string, int64](sessionCap)}
}

func (s *sequencer) next(sess callSession) int64 {
	if sess.perRequest() {
		return s.clock.next()
	}
	return s.counts.update(sess.id, func(prev int64) int64 {
		n := prev + 1
		if sess.origin == "observed" {
			n = max(n, time.Now().UnixMicro())
		}
		return n
	})
}

// lru is a map that drops its least recently used entry past cap, so a
// session in use outlives idle ones.
type lru[K comparable, V any] struct {
	mu    sync.Mutex
	cap   int
	order list.List
	index map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRU[K comparable, V any](cap int) *lru[K, V] {
	return &lru[K, V]{cap: cap, index: map[K]*list.Element{}}
}

// update sets key to f(its value, or V's zero value when absent).
func (l *lru[K, V]) update(key K, f func(V) V) V {
	return l.with(key, func(prev V, _ bool) V { return f(prev) })
}

func (l *lru[K, V]) getOrPut(key K, create func() V) V {
	return l.with(key, func(prev V, found bool) V {
		if found {
			return prev
		}
		return create()
	})
}

// add reports whether key was absent, and adds it.
func (l *lru[K, V]) add(key K) bool {
	added := false
	l.with(key, func(prev V, found bool) V {
		added = !found
		return prev
	})
	return added
}

func (l *lru[K, V]) with(key K, f func(prev V, found bool) V) V {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.index[key]; ok {
		entry := el.Value.(*lruEntry[K, V])
		entry.value = f(entry.value, true)
		l.order.MoveToBack(el)
		return entry.value
	}
	var zero V
	value := f(zero, false)
	l.index[key] = l.order.PushBack(&lruEntry[K, V]{key, value})
	if l.order.Len() > l.cap {
		oldest := l.order.Front()
		l.order.Remove(oldest)
		delete(l.index, oldest.Value.(*lruEntry[K, V]).key)
	}
	return value
}
